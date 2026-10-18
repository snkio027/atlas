"""Finite S3 network acceptance. No publisher, credential reader or recovery path.

The reviewed outer runner supplies target fencing, bounded read waits and kubectl
transport. This module owns probe selection, intent ordering and result meaning.
"""
from dataclasses import dataclass
import copy
import hashlib
import ipaddress
import json
import re


BUSYBOX_IMAGE = 'docker.io/library/busybox:1.38.0@sha256:fd7dc98638c8e305f4dc34e979f1c0fdfdcaeb0fbf8fcff77ae834b6da3d7e6e'


class ContractError(ValueError):
    pass


def require(value, message):
    if not value:
        raise ContractError(message)


def digest(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def select(selector, labels):
    # This finite recipe supports the selectors in Atlas' locked manifests. New
    # expressions require a reviewed adapter, never optimistic interpretation.
    require(isinstance(selector, dict) and not set(selector) - {"matchLabels"}, "unsupported selector")
    wanted = selector.get("matchLabels", {})
    require(isinstance(wanted, dict), "invalid matchLabels")
    return all(labels.get(k) == v for k, v in wanted.items())


def effective(objects, source, destination, port, direction):
    local, remote = (source, destination) if direction == "Egress" else (destination, source)
    namespaces = {o["metadata"]["name"]: o["metadata"].get("labels", {}) for o in objects if o["kind"] == "Namespace"}
    require(local["namespace"] in namespaces and remote["namespace"] in namespaces, "namespace identity missing")
    rules = []
    isolated = False
    for obj in objects:
        if obj["kind"] != "NetworkPolicy" or obj["metadata"].get("namespace") != local["namespace"]:
            continue
        spec = obj["spec"]
        require(not set(spec) - {"podSelector", "policyTypes", "ingress", "egress"}, "unsupported policy field")
        types = spec.get("policyTypes", ["Ingress"] + (["Egress"] if "egress" in spec else []))
        require(set(types) <= {"Ingress", "Egress"}, "invalid policyTypes")
        if direction in types and select(spec["podSelector"], local["labels"]):
            isolated = True
            rules.extend(spec.get(direction.lower(), []))
    if not isolated:
        return True
    permitted = False
    for rule in rules:
        peer_key = "to" if direction == "Egress" else "from"
        require(not set(rule) - {peer_key, "ports"}, "unsupported policy rule")
        port_allowed = not rule.get("ports")
        for entry in rule.get("ports", []):
            require(not set(entry) - {"protocol", "port"}, "unsupported port range")
            require(entry.get("protocol", "TCP") in {"TCP", "UDP", "SCTP"}, "invalid protocol")
            require("port" not in entry or type(entry["port"]) is int, "named port requires a reviewed adapter")
            port_allowed |= entry.get("protocol", "TCP") == "TCP" and entry.get("port", port) == port
        peer_allowed = not rule.get(peer_key)
        for peer in rule.get(peer_key, []):
            require(not set(peer) - {"namespaceSelector", "podSelector"}, "unsupported IP or peer selector")
            ns_allowed = select(peer["namespaceSelector"], namespaces[remote["namespace"]]) if "namespaceSelector" in peer else remote["namespace"] == local["namespace"]
            # An empty peer matches all namespaces and Pods.
            if not peer:
                ns_allowed = True
            peer_allowed |= ns_allowed and select(peer.get("podSelector", {}), remote["labels"])
        permitted |= port_allowed and peer_allowed
    return permitted


def network_identity(objects, namespaces):
    identities = []
    for obj in objects:
        if obj["kind"] == "Namespace" and obj["metadata"]["name"] in namespaces:
            identities.append({"kind": "Namespace", "name": obj["metadata"]["name"], "labels": obj["metadata"].get("labels", {})})
        if obj["kind"] == "NetworkPolicy" and obj["metadata"].get("namespace") in namespaces:
            spec = copy.deepcopy(obj["spec"])
            for key in ["ingress", "egress"]:
                if spec.get(key) == []:
                    del spec[key]
            identities.append({"kind": obj["kind"], "namespace": obj["metadata"]["namespace"], "name": obj["metadata"]["name"], "spec": spec})
    return digest(sorted(identities, key=lambda x: (x["kind"], x.get("namespace", ""), x["name"])))


@dataclass(frozen=True)
class Plan:
    positive: dict
    negative: dict
    destination: dict
    port: int
    network_sha256: str

    @property
    def sha256(self):
        return digest(self.__dict__)


def peer(pod):
    return {"namespace": pod["metadata"]["namespace"], "labels": pod["metadata"].get("labels", {})}


def preflight(objects, positive, negative, destination, port=8333):
    require(type(port) is int and 1 <= port <= 65535, "invalid target port")
    require(positive["kind"] == negative["kind"] == "Pod", "probe must be a Pod")
    require(positive["metadata"]["namespace"] == negative["metadata"]["namespace"], "probe namespaces differ")
    require(positive["metadata"]["name"] != negative["metadata"]["name"], "probe identities collide")
    require(positive["spec"] == negative["spec"], "probe images/runtime differ")
    pl, nl = peer(positive)["labels"], peer(negative)["labels"]
    require(pl.get("atlas.local/s3-client") == "true", "positive peer has no S3 authorization identity")
    require("atlas.local/s3-client" not in nl and "atlas.io/s3-client" not in nl, "negative peer carries authorization")
    require({k: v for k, v in pl.items() if k != "atlas.local/s3-client"} == nl, "probe labels differ beyond authorization")
    for pod in [positive, negative]:
        spec = pod["spec"]
        require(spec.get("automountServiceAccountToken") is False and spec.get("restartPolicy") == "Never", "unsafe probe lifecycle/identity")
        require(not any(spec.get(k) for k in ["hostNetwork", "hostPID", "hostIPC", "initContainers", "ephemeralContainers", "volumes", "serviceAccountName"]), "probe must not acquire credentials or host access")
        containers = spec["containers"]
        require(len(containers) == 1, "probe needs one container")
        c = containers[0]
        require(c["image"] == BUSYBOX_IMAGE, "unsupported probe tool version")
        require(not any(c.get(k) for k in ["env", "envFrom", "volumeMounts"]), "probe cannot consume secrets/config")
        security = c.get("securityContext", {})
        require(security.get("allowPrivilegeEscalation") is False and security.get("readOnlyRootFilesystem") is True and security.get("capabilities") == {"drop": ["ALL"]}, "unsafe container security")
        require(spec.get("securityContext", {}).get("runAsNonRoot") is True and spec["securityContext"].get("seccompProfile") == {"type": "RuntimeDefault"}, "unsafe Pod security")
    for direction in ["Ingress", "Egress"]:
        require(effective(objects, peer(positive), destination, port, direction), "positive peer denied by " + direction)
        require(not effective(objects, peer(negative), destination, port, direction), "negative peer allowed by " + direction)
    return Plan(copy.deepcopy(positive), copy.deepcopy(negative), copy.deepcopy(destination), port,
                network_identity(objects, {peer(positive)["namespace"], destination["namespace"]}))


def pods(image, namespace, prefix):
    require(re.fullmatch(r"[a-z][a-z0-9-]{0,40}", prefix) is not None, "invalid probe prefix")
    spec = {"restartPolicy": "Never", "automountServiceAccountToken": False,
            "nodeSelector": {"node-role.local/compute": "true"},
            "securityContext": {"runAsNonRoot": True, "runAsUser": 65532, "runAsGroup": 65532, "seccompProfile": {"type": "RuntimeDefault"}},
            "containers": [{"name": "probe", "image": image, "imagePullPolicy": "IfNotPresent", "command": ["/bin/sh", "-c", "sleep 600"],
                            "securityContext": {"allowPrivilegeEscalation": False, "readOnlyRootFilesystem": True, "capabilities": {"drop": ["ALL"]}},
                            "resources": {"requests": {"cpu": "50m", "memory": "32Mi"}, "limits": {"cpu": "100m", "memory": "64Mi"}}}]}
    result = []
    for role in ["bound", "unbound"]:
        labels = {"atlas.local/validation": prefix}
        if role == "bound":
            labels["atlas.local/s3-client"] = "true"
        result.append({"apiVersion": "v1", "kind": "Pod", "metadata": {"namespace": namespace, "name": prefix + "-" + role, "labels": labels}, "spec": copy.deepcopy(spec)})
    return result


def classify(result, ip, port, reachable):
    # Match the locked BusyBox invocation, not an arbitrary nonzero command.
    require(type(result.get("exitCode")) is int, "missing process outcome")
    stdout, stderr = result.get("stdout", ""), result.get("stderr", "")
    require(isinstance(stdout, str) and isinstance(stderr, str), "invalid process output")
    require(f"Connecting to {ip}:{port}" in stderr, "request did not target planned endpoint")
    status = re.search(r"(?m)^\s*HTTP/1\.[01] ([1-5][0-9]{2})(?: |\r?$)", stderr)
    if reachable:
        require(status is not None and result["exitCode"] in [0, 1] and "timed out" not in stderr.lower(), "no definite HTTP reachability")
        return {"reachable": True, "httpStatus": int(status[1])}
    require(result["exitCode"] == 1 and status is None and "HTTP/" not in stdout and "HTTP/" not in stderr, "negative reached HTTP or has ambiguous exit")
    require("wget: download timed out" in stderr and not any(x in stderr.lower() for x in ["bad address", "not found", "permission denied", "connection refused", "network is unreachable"]), "not a definite network timeout")
    return {"reachable": False, "connectionTimeout": True}


def run(plan, api, journal):
    """api fences cluster/UID/deadline on every operation; never retries effects.

    snapshot() returns only Namespaces/NetworkPolicies plus the exact current
    Service IP/UID and Ready backend identity. create/wait/execute/delete are
    supplied by the reviewed target adapter. No private state is baked into code.
    """
    def snapshot():
        value = api.snapshot()
        rebuilt = preflight(value["objects"], plan.positive, plan.negative, value["destination"], value["port"])
        require(rebuilt.sha256 == plan.sha256, "network contract drift")
        require(ipaddress.ip_address(value["ip"]).version == 4 and value.get("serviceUID") and value.get("backendUID"), "missing exact live destination")
        return value

    initial = snapshot()  # Reject an invalid positive before creating anything.
    refs = []
    for role, obj in [("positive", plan.positive), ("negative", plan.negative)]:
        journal.claim(role + "-create", obj)
        ref = api.create(copy.deepcopy(obj))
        require(ref.get("uid") and ref.get("namespace") == obj["metadata"]["namespace"] and ref.get("name") == obj["metadata"]["name"], "unknown create result")
        journal.record(role + "-created", ref)
        refs.append(ref)
        api.wait_ready(ref)
    command = ["wget", "-S", "-O", "/dev/null", "-T", "5", f"http://{initial['ip']}:{plan.port}/"]
    facts = {}
    for step, ref, reachable in [("positive-before", refs[0], True), ("negative", refs[1], False), ("positive-after", refs[0], True)]:
        require(snapshot() == initial, "endpoint or policy changed during comparison")
        journal.claim(step, {"pod": ref, "command": command})
        result = api.execute(ref, command)
        journal.record(step + "-result", result)
        facts[step] = classify(result, initial["ip"], plan.port, reachable)
    denial = api.secret_denial()
    require(denial == {"exitCode": 1, "stdout": "no\n", "stderr": ""}, "cross-namespace Secret authorization was not explicitly denied")
    journal.record("secret-denial", denial)
    facts["crossNamespaceSecretDenied"] = True
    # Complete the paired control before cleanup; unknown/failing results leave
    # evidence and Pods for review. Cleanup is not automatic rollback.
    for index, ref in enumerate(refs):
        journal.claim("delete-" + str(index), ref)
        api.delete_exact(ref)
        api.wait_absent(ref)
        journal.record("deleted-" + str(index), ref)
    journal.record("final", {"result": "PASS", "planSHA256": plan.sha256, "serviceUID": initial["serviceUID"], "facts": facts})
    return facts
