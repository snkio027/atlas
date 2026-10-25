#!/usr/bin/env python3
"""Repository-owned offline quality plan, execution evidence and standard CI gate.

Reports are candidate-produced evidence, not an independent assured verifier.
Only the fixed check IDs below can execute commands; JSON is never evaluated.
"""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import signal
import subprocess
import sys
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parent.parent
POLICY = ROOT / "ci/workflow-policy.json"
FAST = ["source", "workflow"]
FULL = FAST + ["race", "render", "ot1", "experiments", "data", "packaging", "web-oci"]
COMMANDS = {
    "source": ["task", "check"],
    "workflow": [sys.executable, "-m", "unittest", "discover", "-s", "scripts/tests", "-v"],
    "race": ["task", "test"],
    "render": ["task", "platform:check"],
    "ot1": ["task", "ot1:contracts"],
    "experiments": ["task", "experiments:check"],
    "data": ["task", "data:contracts"],
    "packaging": [sys.executable, "-m", "unittest", "discover", "-s", "packaging", "-v"],
}


def require(condition, message):
    if not condition:
        raise ValueError(message)


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def encode(value):
    return (json.dumps(value, ensure_ascii=False, sort_keys=True, indent=2) + "\n").encode()


def strict_json(raw):
    def pairs(items):
        result = {}
        for key, value in items:
            require(key not in result, "duplicate JSON key: " + key)
            result[key] = value
        return result
    def invalid_constant(value):
        raise ValueError("non-JSON numeric constant: " + value)
    return json.loads(raw, object_pairs_hook=pairs, parse_constant=invalid_constant)


def save(path, value):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(path.name + ".tmp")
    with temporary.open("xb") as stream:
        stream.write(encode(value))
        stream.flush()
        os.fsync(stream.fileno())
    os.replace(temporary, path)


def git(*args):
    return subprocess.check_output(["git", *args], cwd=ROOT, stderr=subprocess.PIPE)


def policy():
    p = strict_json(POLICY.read_bytes())
    require(set(p) == {"schema", "profile", "assurance", "epoch", "context", "base_branch", "environment", "fast", "full", "standard_reference"}, "unknown policy fields")
    require(type(p["schema"]) is int and p["schema"] == 1 and p["profile"] == "pr-full" and p["assurance"] == "standard" and p["epoch"] == "v1", "unknown policy")
    require(p["context"] == "Delivery / Quality / pr-full-standard-v1" and p["base_branch"] == "main", "unknown admission identity")
    require(p["fast"] == FAST and p["full"] == FULL, "required check coverage changed")
    require(p["environment"] == {"system": "Darwin", "machine": "arm64", "go": "go1.27.2", "runner": "macos-26"}, "unsupported environment contract")
    require(p["standard_reference"] == {"version": "1.1", "sha256": "de77bfd55da3811ed553985db8d61938467a2a3b2c0215e225b607428b667a86"}, "unknown standard reference")
    return p


def environment():
    return {"system": platform.system(), "machine": platform.machine(), "go": subprocess.check_output(["go", "env", "GOVERSION"], text=True).strip()}


def input_digest():
    # Includes tracked, staged, unstaged and non-ignored untracked source files.
    paths = sorted(set(git("ls-files", "-z", "--cached", "--others", "--exclude-standard").split(b"\0")) - {b""})
    h = hashlib.sha256()
    for raw in paths:
        path = ROOT / os.fsdecode(raw)
        h.update(raw + b"\0")
        if path.exists() or path.is_symlink():
            h.update(str(path.lstat().st_mode).encode() + b"\0")
        if path.is_symlink():
            h.update(b"link\0" + os.fsencode(os.readlink(path)))
        elif path.is_file():
            h.update(b"file\0" + hashlib.sha256(path.read_bytes()).digest())
        else:
            h.update(b"deleted\0")
    return h.hexdigest()


def candidate():
    checkout = git("rev-parse", "HEAD").decode().strip()
    event = os.environ.get("CI_EVENT", "local")
    if os.environ.get("GITHUB_ACTIONS") == "true" and event == "local":
        require(os.environ.get("GITHUB_REF", "").startswith("refs/tags/v"), "CI event missing")
        event = "release"
    require(event in ["local", "pull_request", "push", "merge_group", "release"], "unknown event")
    head = os.environ.get("CI_HEAD_SHA") or checkout
    base = os.environ.get("CI_BASE_SHA") or ""
    test = os.environ.get("CI_TEST_SHA") or checkout
    require(test == checkout, "actual checkout differs from test SHA")
    require(re.fullmatch(r"[0-9a-f]{40}", head) is not None, "invalid head SHA")
    if event in ["pull_request", "merge_group"]:
        require(re.fullmatch(r"[0-9a-f]{40}", base) is not None, "base SHA required")
        require(os.environ.get("CI_BASE_REF") in ["main", "refs/heads/main"], "unexpected base branch")
    if event == "pull_request":
        parents = git("rev-list", "--parents", "-n", "1", "HEAD").decode().strip().split()[1:]
        require(parents == [base, head], "checkout is not the declared base/head test merge")
    dirty = bool(git("status", "--porcelain"))
    require(event == "local" or not dirty, "CI checkout is dirty")
    return {"event": event, "head": head, "base": base, "test": test, "tree": git("rev-parse", "HEAD^{tree}").decode().strip(), "dirty": dirty, "inputs_sha256": input_digest()}


def binding(p, current):
    return {"candidate": current, "policy_sha256": digest(POLICY.read_bytes()), "verifier_sha256": digest(Path(__file__).read_bytes()), "profile": p["profile"], "assurance": p["assurance"], "epoch": p["epoch"], "environment": p["environment"]}


def make_plan(p, current):
    base = current["base"]
    if not base:
        try:
            base = git("rev-parse", "origin/main").decode().strip()
        except subprocess.CalledProcessError:
            base = ""
    merge_base, changed = None, []
    reason = "Conservative Full: no proven dependency selector; all paths require the complete contract."
    if base:
        try:
            merge_base = git("merge-base", base, current["head"]).decode().strip()
            # No rename detection: both deletion and addition remain in the change set.
            names = git("diff", "--name-only", "--no-renames", "-z", merge_base, current["head"])
            changed = sorted(set(os.fsdecode(x) for x in names.split(b"\0") if x))
        except subprocess.CalledProcessError:
            reason = "Full fallback: comparison history is unavailable."
    else:
        reason = "Full fallback: no comparison base is available."
    if current["dirty"]:
        names = git("diff", "--name-only", "-z", "HEAD") + git("ls-files", "--others", "--exclude-standard", "-z")
        changed = sorted(set(changed) | {os.fsdecode(x) for x in names.split(b"\0") if x})
    return {"schema": 1, **binding(p, current), "comparison": {"base": base, "head": current["head"], "merge_base": merge_base, "paths": changed}, "full_required": True, "checks": p["full"], "reason": reason}


def validate_plan(plan, p, current):
    require(plan == make_plan(p, current), "plan is malformed, stale, incomplete or bound to another candidate")
    require(type(plan["schema"]) is int, "invalid plan schema type")
    require(type(plan["full_required"]) is bool and plan["full_required"], "Full must be explicit")


def run_command(command, log, env):
    process = subprocess.Popen(command, cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
    try:
        code = process.wait(timeout=1800)
        require(code == 0, "check exited " + str(code))
    except BaseException:
        if process.poll() is None:
            os.killpg(process.pid, signal.SIGTERM)
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait()
        raise


def web_oci(log, env):
    with tempfile.TemporaryDirectory(prefix="atlas-quality-oci-") as directory:
        output = Path(directory)
        target = dict(env, CGO_ENABLED="0", GOOS="linux", GOARCH="arm64")
        run_command(["go", "build", "-trimpath", "-buildvcs=false", "-ldflags=-buildid=", "-o", str(output / "web"), "./cmd/atlas-web"], log, target)
        for name in ["first", "second"]:
            run_command(["go", "run", "./cmd/atlas-web-image", "--binary", str(output / "web"), "--out", str(output / (name + ".oci.tar"))], log, env)
        require((output / "first.oci.tar").read_bytes() == (output / "second.oci.tar").read_bytes(), "Web OCI output is not deterministic")


def validate_title(title):
    require(isinstance(title, str) and re.fullmatch(r"(?:feat|fix|docs|style|refactor|perf|test|build|ci|chore|revert)(?:\([a-z0-9][a-z0-9._/-]*\))?!?: [^\n]+", title) is not None, "PR title must express a Conventional Commit intent")


def execute(mode):
    require(mode in ["fast", "full"], "unknown execution mode")
    output = Path(os.environ.get("CI_REPORT_DIR") or ROOT / ".state/quality" / (datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%S") + "-" + uuid.uuid4().hex[:8]))
    output.mkdir(parents=True, exist_ok=True)
    report_path = output / (mode + ".json")
    require(not report_path.exists(), "do not overwrite prior execution evidence")
    report = {"schema": 1, "mode": mode, "result": "failure", "checks": [], "started_at": datetime.datetime.now(datetime.timezone.utc).isoformat(), "run_id": os.environ.get("GITHUB_RUN_ID"), "attempt": os.environ.get("GITHUB_RUN_ATTEMPT"), "workflow_ref": os.environ.get("GITHUB_WORKFLOW_REF"), "runner_image": {"name": os.environ.get("ImageOS"), "version": os.environ.get("ImageVersion")}}
    start = time.monotonic()
    try:
        p, current = policy(), candidate()
        report.update(binding(p, current))
        if current["event"] == "pull_request":
            validate_title(os.environ.get("CI_PR_TITLE"))
        actual_environment = environment()
        require(actual_environment == {k: p["environment"][k] for k in ["system", "machine", "go"]}, "unsupported execution environment")
        report["actual_environment"] = actual_environment
        if mode == "full" and (current["event"] == "pull_request" or os.environ.get("CI_PLAN_PATH")):
            plan_path = Path(os.environ.get("CI_PLAN_PATH", "/missing-quality-plan"))
            plan = strict_json(plan_path.read_bytes())
            validate_plan(plan, p, current)
            report["plan_sha256"] = digest(plan_path.read_bytes())
            if plan_path.resolve() != (output / "plan.json").resolve():
                save(output / "plan.json", plan)
        env = dict(os.environ, GOTOOLCHAIN="local", GOPROXY="off", GOSUMDB="off", GOFLAGS="-mod=vendor", PYTHONDONTWRITEBYTECODE="1")
        for check in p[mode]:
            record = {"id": check, "result": "failure"}
            report["checks"].append(record)
            before = time.monotonic()
            print("START " + check, flush=True)
            try:
                with (output / (check + ".log")).open("xb") as log:
                    if check == "web-oci":
                        web_oci(log, env)
                    else:
                        run_command(COMMANDS[check], log, env)
                record["result"] = "success"
            finally:
                record["seconds"] = round(time.monotonic() - before, 3)
            print("PASS " + check, flush=True)
        require(candidate() == current, "source inputs changed during execution")
        report["result"] = "success"
    except BaseException as error:
        report["error"] = str(error)
        raise
    finally:
        report["seconds"] = round(time.monotonic() - start, 3)
        report["finished_at"] = datetime.datetime.now(datetime.timezone.utc).isoformat()
        save(report_path, report)
        print("Evidence: " + str(report_path), flush=True)


def validate_report(report, mode, p, current, run_id, attempt):
    require(type(report.get("schema")) is int and report.get("schema") == 1 and report.get("mode") == mode and report.get("result") == "success", "missing or failed " + mode + " report")
    for key, value in binding(p, current).items():
        require(report.get(key) == value, "report binding differs: " + key)
    require(report.get("run_id") == run_id and report.get("attempt") == attempt, "report from another run/attempt")
    require(report.get("actual_environment") == {k: p["environment"][k] for k in ["system", "machine", "go"]}, "report execution environment differs")
    checks = report.get("checks", [])
    require([x.get("id") for x in checks] == p[mode], "missing, duplicated or unexpected checks")
    require(all(x.get("result") == "success" and type(x.get("seconds")) in [int, float] and x["seconds"] >= 0 for x in checks), "check did not succeed")


def phase_gate(event, draft, fast, validate, plan, execution):
    require(event in ["pull_request", "push", "merge_group"], "event cannot admit")
    require(fast == validate == execution == "success", "required execution failed, skipped or absent")
    if event == "pull_request":
        require(draft == "false" and plan == "success", "Draft or missing plan cannot admit")
    else:
        require(plan == "skipped", "unexpected plan state")


def validate_current_pr(live, current):
    validate_title(live.get("title"))
    require(live.get("title") == os.environ.get("CI_PR_TITLE"), "PR metadata changed during execution")
    require(live["state"] == "open" and live["draft"] is False and live["head"]["sha"] == current["head"] and live["base"]["sha"] == current["base"] and live["base"]["ref"] == "main", "PR candidate or phase is no longer current")


def gate(directory):
    p, current = policy(), candidate()
    phase_gate(current["event"], os.environ.get("CI_PR_DRAFT"), os.environ.get("FAST_RESULT"), os.environ.get("VALIDATE_RESULT"), os.environ.get("PLAN_RESULT"), os.environ.get("EXECUTION_RESULT"))
    for mode in ["fast", "full"]:
        report = strict_json((directory / mode / (mode + ".json")).read_bytes())
        validate_report(report, mode, p, current, os.environ.get("GITHUB_RUN_ID"), os.environ.get("GITHUB_RUN_ATTEMPT"))
        if mode == "full" and current["event"] == "pull_request":
            raw = (directory / mode / "plan.json").read_bytes()
            validate_plan(strict_json(raw), p, current)
            require(report.get("plan_sha256") == digest(raw), "execution did not consume this plan")
    if current["event"] == "pull_request":
        repo = os.environ.get("GITHUB_REPOSITORY", "")
        number = os.environ.get("CI_PR_NUMBER", "")
        require(re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repo) and number.isdigit(), "invalid PR identity")
        live = strict_json(subprocess.check_output(["gh", "api", "repos/" + repo + "/pulls/" + number], stderr=subprocess.PIPE))
        validate_current_pr(live, current)
    print("Standard PR-full evidence PASS; owner review and repository rules still apply.")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("command", choices=["plan", "fast", "full", "gate", "phase"])
    parser.add_argument("--reports", type=Path)
    args = parser.parse_args()
    if args.command == "plan":
        path = os.environ.get("CI_PLAN_PATH")
        require(bool(path), "CI_PLAN_PATH is required")
        require(not Path(path).exists(), "do not overwrite an existing plan")
        save(Path(path), make_plan(policy(), candidate()))
        print("Full coverage plan: " + path)
    elif args.command == "phase":
        phase_gate(os.environ.get("CI_EVENT"), os.environ.get("CI_PR_DRAFT"), os.environ.get("FAST_RESULT"), os.environ.get("VALIDATE_RESULT"), os.environ.get("PLAN_RESULT"), os.environ.get("EXECUTION_RESULT"))
    elif args.command == "gate":
        require(args.reports is not None, "reports directory required")
        gate(args.reports)
    else:
        execute(args.command)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, subprocess.SubprocessError) as error:
        print("Quality refused: " + str(error), file=sys.stderr)
        sys.exit(1)
