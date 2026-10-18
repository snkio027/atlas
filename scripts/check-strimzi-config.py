#!/usr/bin/env python3
"""Check the locked Strimzi configuration parser offline, without Kubernetes.

Requires the pinned operator image already present in the selected Docker
context. The positive case reaches Main's post-buildFromMap log; subsequent
API discovery is deliberately unavailable. This is not operator readiness.
"""
import argparse
import json
from pathlib import Path
import subprocess
import tempfile

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--docker-context", default="orbstack")
args = parser.parse_args()
root = Path(__file__).resolve().parents[1]
render = root / "gitops/platform/data/kafka-operator/overlays/development/rendered.yaml"
objects = [json.loads(part) for part in render.read_text().split("\n---\n")]
deployment = next(o for o in objects if o["kind"] == "Deployment")
container = deployment["spec"]["template"]["spec"]["containers"][0]
config = next(o for o in objects if o["kind"] == "ConfigMap")["data"]
image = container["image"]
assert "@sha256:" in image
docker = ["docker", "--context", args.docker_context]
maps = ("STRIMZI_KAFKA_IMAGES", "STRIMZI_KAFKA_CONNECT_IMAGES", "STRIMZI_KAFKA_MIRROR_MAKER_2_IMAGES")
evidence = root / ".state/latest/data-platform/strimzi-config"
evidence.mkdir(parents=True, exist_ok=True)
with tempfile.TemporaryDirectory(prefix="atlas-strimzi-config-", dir="/private/tmp") as temp:
    directory = Path(temp)
    directory.chmod(0o755)
    for name, value in config.items():
        assert Path(name).name == name
        (directory / name).write_text(value)
        (directory / name).chmod(0o644)
    for case in ("single-version-rejected", "complete-catalog-accepted"):
        env = {item["name"]: item["value"] for item in container["env"] if "value" in item}
        if case == "single-version-rejected":
            for name in maps:
                env[name] = next(line for line in env[name].splitlines() if line.startswith("4.3.1="))
        env.update(STRIMZI_LEADER_ELECTION_IDENTITY="offline-config-check", KUBERNETES_MASTER="http://127.0.0.1:1")
        create = docker + ["create", "--pull=never", "--network=none", "--hostname=localhost", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--user=1001", "--memory=512m", "--tmpfs=/tmp:rw,nosuid,size=128m", "--volume=" + str(directory) + ":/opt/strimzi/custom-config:ro"]
        for name, value in sorted(env.items()):
            create.extend(["--env", name + "=" + value])
        create.extend([image, *container["args"]])
        cid = subprocess.check_output(create, stderr=subprocess.PIPE, timeout=30).decode().strip()
        try:
            started = subprocess.Popen(docker + ["start", "--attach", cid], stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
            try:
                output, _ = started.communicate(timeout=20)
            except subprocess.TimeoutExpired:
                subprocess.run(docker + ["stop", "--time=5", cid], check=True, stdout=subprocess.DEVNULL, timeout=15)
                output, _ = started.communicate(timeout=10)
            (evidence / (case + ".log")).write_bytes(output)
            parsed = b"Cluster Operator configuration is " in output
            if case == "single-version-rejected":
                assert not parsed and b"No image for version 4.2.0" in output, "negative control did not reproduce the startup defect"
            else:
                assert parsed and b"InvalidConfigurationException" not in output, "complete catalog did not pass the actual operator parser"
            print(case + ": PASS", flush=True)
        finally:
            subprocess.run(docker + ["rm", "--force", cid], check=True, stdout=subprocess.DEVNULL, timeout=30)
(evidence / "result.json").write_text(json.dumps({"result": "PASS", "image": image, "configurationParser": True, "kubernetesAccess": False, "operatorReadinessProven": False}, indent=2) + "\n")
