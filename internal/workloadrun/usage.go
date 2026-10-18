package workloadrun

import (
	"atlas-refactor/internal/platform"
	"atlas-refactor/internal/workload"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Preview contains authored intent and effects, never decrypted credentials.
type Preview struct {
	Target            string           `json:"target"`
	Repository        string           `json:"repository"`
	Branch            string           `json:"branch"`
	PlanSHA256        string           `json:"planSHA256"`
	Before            *workload.Intent `json:"before,omitempty"`
	After             workload.Intent  `json:"after"`
	Phases            []string         `json:"phases"`
	CredentialTargets []string         `json:"credentialTargets"`
	Operations        []string         `json:"operations"`
}

func (w *Workflow) Preview(ctx context.Context, p Plan) (Preview, error) {
	v := Preview{Target: p.Cluster, Repository: p.Repository, Branch: p.Branch, PlanSHA256: workload.Digest(workload.JSON(p)),
		After: w.Model.Intent, Phases: p.Phases, CredentialTargets: p.CredentialTargets,
		Operations: []string{"import verified image into four instance nodes", "publish exact-parent Git commits and wait for read-only Gates", "GET workload /readyz over verified TLS; read declared metrics", "business functionality remains UNPROVEN; no functional probe"}}

	dir := filepath.Join(w.Config.StateDirectory, "authority", v.PlanSHA256)
	complete, err := attemptComplete(dir)
	if err != nil {
		return v, err
	}
	if complete && w.application() {
		if err = w.validateApplicationFinal(p); err != nil {
			return v, err
		}
		v.Before = &v.After
		v.Phases = []string{}
		v.CredentialTargets = []string{}
		v.Operations = []string{"already completed: read-only convergence, HTTPS readiness and declared metrics", "no image import, credential preparation, Git publication or functional probe"}
		return v, nil
	}
	if p.Parent != p.BaseCommit {
		files, err := w.ReadTree(ctx, p.Parent)
		if err != nil {
			return v, err
		}
		prior := workload.Intent{Workloads: []workload.Workload{}, Bindings: []workload.Binding{}}
		for name, raw := range files {
			if strings.HasPrefix(name, "platform/projects/") {
				if err = workload.StrictDecode(raw, &prior.Project); err != nil {
					return v, err
				}
			}
			if strings.HasPrefix(name, "platform/workloads/") {
				var x workload.Workload
				if err = workload.StrictDecode(raw, &x); err != nil {
					return v, err
				}
				prior.Workloads = append(prior.Workloads, x)
			}
			if strings.HasPrefix(name, "platform/bindings/") {
				var x workload.Binding
				if err = workload.StrictDecode(raw, &x); err != nil {
					return v, err
				}
				prior.Bindings = append(prior.Bindings, x)
			}
		}
		m, err := workload.Resolve(prior, true)
		if err != nil {
			return v, err
		}
		v.Before = &m.Intent
	}
	if len(p.CredentialTargets) > 0 {
		v.Operations = append(v.Operations, "verify instance Trust Root backup; prepare/reuse independent Binding credentials; publish strict ciphertext to public Git (plaintext stays private)")
	}
	return v, nil
}

type Status struct {
	UnpublishedPlan  string       `json:"unpublishedPlan,omitempty"`
	Target           string       `json:"target"`
	Repository       string       `json:"repository"`
	Branch           string       `json:"branch"`
	Authority        string       `json:"authority"`
	Phase            string       `json:"phase"`
	Revision         string       `json:"revision"`
	ObservedAt       string       `json:"observedAt"`
	Current          string       `json:"current"`
	HistoricalResult string       `json:"historicalResult"`
	Functional       string       `json:"functional"`
	CredentialsPath  string       `json:"credentialsPath"`
	Services         []string     `json:"services"`
	Completed        []string     `json:"completed"`
	UncertainWrites  []string     `json:"uncertainWrites"`
	Next             []string     `json:"next"`
	Observation      *Observation `json:"observation,omitempty"`
}

func (w *Workflow) Current(ctx context.Context) (s Status, resultErr error) {
	s = Status{UnpublishedPlan: w.UnpublishedPlan, Target: w.Install.Config.Cluster, Repository: w.Context.Repository, Branch: w.Context.Branch, Authority: "UNKNOWN", Current: "UNKNOWN",
		HistoricalResult: "NONE", Functional: "UNPROVEN", ObservedAt: time.Now().UTC().Format(time.RFC3339), CredentialsPath: filepath.Join(w.Config.StateDirectory, "keys.json"),
		Services: []string{}, Completed: []string{}, UncertainWrites: []string{}, Next: []string{}}
	for _, v := range w.Model.Intent.Workloads {
		s.Services = append(s.Services, v.Name)
	}
	p, err := w.ReadPlan()
	if err != nil {
		return s, err
	}
	if p.ConfigSHA256 != workload.Digest(workload.JSON(w.Config)) || p.IntentSHA256 != workload.Digest(workload.JSON(w.Model.Intent)) ||
		p.ClusterUID != w.Install.Record.ClusterUID || p.InstallID != w.Install.Record.InstallID || p.CompilerSHA256 != w.BinarySHA256 {
		return s, errors.New("status requires the recorded inputs and compiler; use the workspace's recorded plan")
	}
	dir := filepath.Join(w.Config.StateDirectory, "authority", workload.Digest(workload.JSON(p)))
	complete, terminalErr := attemptComplete(dir)
	if terminalErr != nil {
		s.HistoricalResult = "STOP_OR_CONTRADICTION"
		s.Next = []string{"read-only status and logs are allowed", "inspect retained intents/receipts; do not republish, clear STOP, regenerate credentials or rebuild as recovery"}
	} else if complete {
		raw, err := regular(filepath.Join(dir, "final.json"), true)
		if err != nil {
			return s, err
		}
		var f Object
		if err = workload.StrictDecode(raw, &f); err != nil {
			return s, err
		}
		if f["planSHA256"] != workload.Digest(workload.JSON(p)) || f["compilerSHA256"] != p.CompilerSHA256 {
			return s, errors.New("final binding differs")
		}
		if w.application() {
			if err = w.validateApplicationFinal(p); err != nil {
				return s, err
			}
		}
		s.HistoricalResult = str(f["result"])
		if s.HistoricalResult == "PASS" {
			s.Functional = "HISTORICAL_PASS"
		}
	}
	parent := p.Parent
	var latest *workload.Result
	gap := false
	for _, phase := range p.Phases {
		path := w.publicationPath(p, phase)
		_, err := regular(path, true)
		if os.IsNotExist(err) {
			gap = true
			if _, e := regular(path+".intent", true); e == nil {
				s.UncertainWrites = append(s.UncertainWrites, phase)
			} else if !os.IsNotExist(e) {
				return s, e
			}
			continue
		}
		if err != nil {
			return s, err
		}
		if gap {
			return s, errors.New("publication receipts are not a contiguous prefix")
		}
		r, err := w.Compile(phase)
		if err != nil {
			return s, err
		}
		receipt, err := w.readPublication(p, phase, parent, platform.BundleDigest(r.Files))
		if err != nil {
			return s, err
		}
		s.Completed = append(s.Completed, phase)
		s.Phase = phase
		s.Revision = receipt.Commit
		parent = receipt.Commit
		latest = &r
	}
	if len(s.UncertainWrites) > 0 {
		s.Next = []string{"publication outcome unknown: inspect Git and retained intent; no replay"}
		return s, errors.New("unreceipted publication intent")
	}
	// Local history survives an unavailable live read; it never proves readiness.
	if err = w.bind(ctx); err != nil {
		return s, err
	}
	if err = w.authority(ctx); err != nil {
		return s, err
	}
	s.Authority = "ADOPTED"
	if latest == nil {
		s.Current = "NOT_PUBLISHED"
		s.Next = append(s.Next, "review plan before deployment")
		return s, terminalErr
	}
	report, err := w.Observe(ctx, *latest, s.Revision)
	s.Observation = &Observation{Schema: report.Schema, Phase: report.Phase, ClusterUID: report.ClusterUID, Revision: report.Revision, Project: report.Project, Workload: report.Workload, Binding: report.Binding, Runtime: report.Runtime}
	if err != nil {
		s.Current = "NOT_READY"
		return s, err
	}
	s.Current = "CONVERGED"
	if s.Phase == "consumer" && w.application() {
		if err = w.applicationReady(ctx); err != nil {
			s.Current = "NOT_READY"
			return s, err
		}
		s.Current = "READY"
	}
	if terminalErr != nil {
		return s, terminalErr
	}
	s.Next = append(s.Next, "open a listed service or request logs; business tests are separate")
	return s, nil
}

// Open provides explicit loopback access; it never changes hosts, CA trust or
// kubeconfig. Host validation prevents browser DNS rebinding to the proxy.
func (w *Workflow) OpenApplication(ctx context.Context, name string, port int, announce func(string)) error {
	if port < 0 || port > 65535 || port > 0 && port < 1024 {
		return errors.New("local port must be 0 (allocated) or 1024..65535")
	}
	status, err := w.Current(ctx)
	if err != nil {
		return err
	}
	if status.Current != "READY" {
		return errors.New("application is not yet published and ready; inspect app status")
	}
	var host string
	for _, v := range w.Model.Intent.Workloads {
		if v.Name == name {
			host = v.Exposure.Hostname
		}
	}
	if host == "" {
		return errors.New("unknown application; run app status to list services")
	}
	client, err := w.httpsClient(host)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	target, _ := url.Parse("https://" + host)
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = client.Transport
	director := proxy.Director
	proxy.Director = func(r *http.Request) { director(r); r.Host = host }
	proxy.ErrorHandler = func(rw http.ResponseWriter, r *http.Request, e error) {
		http.Error(rw, "verified upstream unavailable; inspect app status", http.StatusBadGateway)
	}
	listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return err
	}
	address := listener.Addr().String()
	handler := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Host != address {
			http.Error(rw, "loopback Host required", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+address {
			http.Error(rw, "foreign Origin denied", http.StatusForbidden)
			return
		}
		proxy.ServeHTTP(rw, r)
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	if announce != nil {
		announce(fmt.Sprintf("%s / %s: http://%s (loopback HTTP; upstream %s TLS verified; Ctrl-C closes)", w.Install.Config.Cluster, name, address, host))
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
		_ = server.Close()
		<-done
		return nil
	case err := <-done:
		return err
	}
}

func (w *Workflow) ApplicationLogs(ctx context.Context, name string, out io.Writer) error {
	if err := w.bind(ctx); err != nil {
		return err
	}
	if err := w.authority(ctx); err != nil {
		return err
	}
	for _, v := range w.Model.Intent.Workloads {
		if v.Name != name {
			continue
		}
		// Logs must remain available for a non-ready/crashing workload. Select
		// only this authored workload's Pods; no exec or broad namespace read.
		raw, err := w.kube(ctx, "get", "pods", "-n", v.Namespace(), "-l", "atlas.io/project="+v.Project+",atlas.io/workload="+v.Name, "-o", "json")
		if err != nil {
			return err
		}
		pod, err := logPod(raw, v)
		if err != nil {
			return err
		}

		tool, args, err := w.kubeCommand("logs", "-n", v.Namespace(), str(at(pod, "metadata", "name")), "--tail=100", "--timestamps=true")
		if err != nil {
			return err
		}
		cmd := exec.CommandContext(ctx, tool, args...)
		cmd.Stdout = out
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "LANG=C"}
		if err = cmd.Run(); err != nil {
			return errors.New("log read failed; command output withheld")
		}
		return nil
	}
	return errors.New("unknown workload")
}

func logPod(raw []byte, v workload.Workload) (Object, error) {
	// Kubernetes responses legitimately contain null (for example a condition's
	// lastProbeTime). Authored intent's no-null rule does not apply to live data.
	var list Object
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	pods := array(list["items"])
	if len(pods) == 0 {
		return nil, errors.New("no workload Pods yet; inspect app status")
	}
	var pod Object
	for _, item := range pods {
		o := mapping(item)
		if at(o, "metadata", "namespace") != v.Namespace() || str(at(o, "metadata", "uid")) == "" || at(o, "spec", "serviceAccountName") != v.Name {
			return nil, errors.New("log Pod identity differs")
		}
		if pod == nil || str(at(o, "metadata", "name")) < str(at(pod, "metadata", "name")) {
			pod = o
		}
	}
	return pod, nil
}

// ObservationView is a source-bound snapshot for returning users after a new
// review has been prepared. Every execution approval rejects this view.
func (w *Workflow) ObservationView(p Plan) (*Workflow, error) {
	if p.ConfigSHA256 != workload.Digest(workload.JSON(w.Config)) || p.IntentSHA256 != workload.Digest(workload.JSON(w.Model.Intent)) || p.CompilerSHA256 != w.BinarySHA256 || p.ClusterUID != w.Install.Record.ClusterUID {
		return nil, errors.New("recorded plan/input/compiler mismatch")
	}
	clone := *w
	clone.observationPlan = &p
	return &clone, nil
}
