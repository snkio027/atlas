package workloadrun

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"github.com/snkio027/atlas/internal/installation"
	"github.com/snkio027/atlas/internal/webslice"
	"github.com/snkio027/atlas/internal/workload"
	"net/http"
	"path/filepath"
)

func (w *Workflow) verifyLegacy(ctx context.Context) error {
	raw, e := regular(filepath.Join(w.Install.Config.StateDirectory, "credentials.json"), true)
	if e != nil {
		return e
	}
	if workload.Digest(raw) != w.Install.Record.CredentialsSHA256 {
		return errors.New("D1 credentials changed")
	}
	var c installation.Credentials
	if e = installation.Decode(raw, &c); e != nil {
		return e
	}
	address, stop, e := w.forwardService(ctx, "s3")
	if e != nil {
		return e
	}
	defer stop()
	client := loopbackClient()
	defer client.CloseIdleConnections()
	s3 := webslice.Client{Endpoint: address, Bucket: "uploads", Access: c.AccessKey, Secret: c.SecretKey, HTTP: client}
	body, code, e := s3.Request(ctx, "GET", "/uploads?list-type=2", nil)
	if e != nil || code != 200 {
		return errors.New("existing D1 S3 identity stopped working")
	}
	var list struct {
		XMLName xml.Name
		Name    string
	}
	if xml.Unmarshal(body, &list) != nil || list.XMLName.Local != "ListBucketResult" || list.Name != "uploads" {
		return errors.New("legacy S3 bucket identity differs")
	}
	web, e := w.httpsClient("web.atlas.test")
	if e != nil {
		return e
	}
	defer web.CloseIdleConnections()
	req, e := http.NewRequestWithContext(ctx, "GET", "https://web.atlas.test/", nil)
	if e != nil {
		return e
	}
	response, e := web.Do(req)
	if e != nil {
		return errors.New("legacy D1 HTTPS endpoint unavailable")
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("legacy D1 HTTPS status %d", response.StatusCode)
	}
	return nil
}
