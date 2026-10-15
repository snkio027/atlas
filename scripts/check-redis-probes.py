#!/usr/bin/env python3
"""Run the declared Redis probes against its locked image, without Kubernetes.

Uses ephemeral test credentials and TLS material, no network or host ports.
The image must already exist locally. This is not a live persistence test.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--docker-context", default="orbstack")
parser.add_argument("--openssl", default="openssl")
args = parser.parse_args()
root = Path(__file__).resolve().parents[1]
objects = json.loads((root / "platform/development/capabilities/resources/redis.json").read_text())["items"]
config = next(o["data"] for o in objects if o["kind"] == "ConfigMap")
container = next(o for o in objects if o["kind"] == "StatefulSet")["spec"]["template"]["spec"]["containers"][0]
assert "@sha256:" in container["image"]
docker = ["docker", "--context", args.docker_context]


def command(argv):
    return subprocess.run(argv, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30)


with tempfile.TemporaryDirectory(prefix="atlas-redis-probes-", dir="/private/tmp") as temp:
    directory = Path(temp)
    for folder in ("tls", "auth", "config"):
        (directory / folder).mkdir(mode=0o755)
    tls = directory / "tls"
    cert = command([args.openssl, "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", str(tls / "tls.key"), "-out", str(tls / "tls.crt"), "-days", "1", "-subj", "/CN=redis.redis.svc.cluster.local", "-addext", "subjectAltName=DNS:redis.redis.svc.cluster.local"])
    cert.check_returncode()
    (tls / "ca.crt").write_bytes((tls / "tls.crt").read_bytes())
    password = os.urandom(32).hex()
    (directory / "auth/users.acl").write_text("user default off\nuser platform-admin on #" + hashlib.sha256(password.encode()).hexdigest() + " ~* &* +@all\n")
    (directory / "env").write_text("REDISCLI_AUTH=" + password + "\n")
    (directory / "env").chmod(0o600)
    for name, data in config.items():
        assert Path(name).name == name
        (directory / "config" / name).write_text(data)
    for folder in ("tls", "auth", "config"):
        for file in (directory / folder).iterdir():
            file.chmod(0o444)
    argv = docker + ["run", "--detach", "--pull=never", "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--user=999:999", "--memory=512m", "--env-file=" + str(directory / "env"), "--tmpfs=/tmp:rw,nosuid,size=16m,uid=999,gid=999", "--tmpfs=/data:rw,nosuid,size=64m,uid=999,gid=999"]
    for folder, target in (("tls", "/tls"), ("auth", "/auth"), ("config", "/etc/redis")):
        argv += ["--mount", "type=bind,source=" + str(directory / folder) + ",target=" + target + ",readonly"]
    created = command(argv + [container["image"], *container["args"]]); created.check_returncode()
    cid = created.stdout.decode().strip()
    try:
        startup = container["startupProbe"]["exec"]["command"]
        deadline = time.monotonic() + 20
        while True:
            probe = command(docker + ["exec", cid, *startup])
            if probe.returncode == 0 and probe.stdout.strip() == b"PONG":
                break
            assert time.monotonic() < deadline, "declared startup probe failed"
            time.sleep(0.2)
        for name in ("startupProbe", "readinessProbe", "livenessProbe"):
            probe = command(docker + ["exec", cid, *container[name]["exec"]["command"]])
            assert probe.returncode == 0 and probe.stdout.strip() == b"PONG", name
        invalid = startup[:-3] + ["ping", *startup[-3:-1]]
        negative = command(docker + ["exec", cid, *invalid])
        assert negative.returncode != 0 and b"wrong number of arguments" in negative.stdout + negative.stderr
        denied = command(docker + ["exec", "--env=REDISCLI_AUTH=incorrect-test-password", cid, *startup])
        assert denied.returncode != 0 and b"PONG" not in denied.stdout
    finally:
        cleanup = command(docker + ["rm", "--force", cid]); cleanup.check_returncode()
evidence = root / ".state/latest/data-platform/redis-probes"
evidence.mkdir(parents=True, exist_ok=True)
result = {"result": "PASS", "image": container["image"], "declaredProbes": 3, "trailingOptionRejected": True, "wrongPasswordRejected": True, "kubernetesAccess": False}
(evidence / "result.json").write_text(json.dumps(result, indent=2) + "\n")
print("Redis declared TLS/auth probes: PASS; trailing-option and wrong-password controls: PASS")
