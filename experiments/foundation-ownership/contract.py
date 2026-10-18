#!/usr/bin/env python3
"""OT-1 offline scope and phase contract. No cluster, credentials or Git writes."""
import copy
import hashlib
import json
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[2]
BASE = Path(__file__).resolve().parent
OLD_SHA = 'b618dea24b7c46cd36fd11a568a72c9a88f2097a'
NEW_SHA = 'b5d0562381f5b3989578d62e2677364d8c73710d'
CLUSTER = 'atlas-refactor-test-ot1'
OLD = 'capability-foundation'
DOMAINS = ('secrets', 'observability', 'storage')
COUNTS = (3, 4, 6)
OLD_PATH = 'gitops/platform/foundation/capabilities/overlays/development/resources.json'
NEW_PATHS = {d: f'gitops/platform/foundation/{d}/overlays/development/resources.json' for d in DOMAINS}
TRACKING = 'argocd.argoproj.io/tracking-id'


def require(value, message):
    if not value:
        raise ValueError(message)


def encoded(value):
    return (json.dumps(value, indent=2, sort_keys=True) + '\n').encode()


def digest(value):
    return hashlib.sha256(encoded(value)).hexdigest()


def key(obj):
    meta = obj['metadata']
    return '/'.join((obj['apiVersion'], obj['kind'], meta.get('namespace', ''), meta['name']))


def index(objects):
    result = {key(o): o for o in objects}
    require(len(result) == len(objects), 'duplicate resource identity')
    return result


def git_file(sha, path):
    return subprocess.check_output(['git', 'show', f'{sha}:{path}'], cwd=ROOT, timeout=30)


def read_list(data):
    value = json.loads(data)
    require(value['apiVersion'] == 'v1' and value['kind'] == 'List', 'expected Kubernetes List')
    index(value['items'])
    return value['items']


def semantic(obj):
    result = copy.deepcopy(obj)
    result.pop('status', None)
    meta = result['metadata']
    for field in ('uid', 'resourceVersion', 'creationTimestamp', 'managedFields', 'generation'):
        meta.pop(field, None)
    annotations = meta.get('annotations', {})
    annotations.pop(TRACKING, None)
    if not annotations:
        meta.pop('annotations', None)
    return result


def scope(old, domains):
    """Check exact identity/content equality, not just object counts/kinds."""
    originals = index(old)
    expected = {}
    ownership = {}
    for domain, count in zip(DOMAINS, COUNTS):
        objects = domains[domain]
        require(len(objects) == count, f'wrong {domain} object count')
        for identity, obj in index(objects).items():
            require(identity not in expected, 'overlapping domain resource: ' + identity)
            require(obj['apiVersion'] == ('networking.k8s.io/v1' if obj['kind'] == 'NetworkPolicy' else 'v1'), 'unexpected API version')
            require(obj['kind'] in ('Namespace', 'ResourceQuota', 'LimitRange', 'NetworkPolicy'), 'out-of-scope kind')
            require(TRACKING not in obj['metadata'].get('annotations', {}), 'Git must not author tracking')
            expected[identity] = obj
            ownership[identity] = domain + '-foundation'
    require(len(originals) == 13 and originals == expected, 'old/new identity or content differs')
    require(set(ownership) == set(EXACT_IDENTITIES), 'not the reviewed 13 objects')
    for identity, domain in EXACT_IDENTITIES.items():
        require(ownership[identity] == domain + '-foundation', 'wrong domain assignment: ' + identity)
    return [{'identity': k, 'oldOwner': OLD, 'newOwner': ownership[k], 'desiredSHA256': digest(originals[k])}
            for k in sorted(originals)]


EXACT_IDENTITIES = {}
for _domain, _namespace in zip(DOMAINS, ('atlas-secrets', 'atlas-monitoring', 'atlas-storage')):
    EXACT_IDENTITIES['v1/Namespace//' + _namespace] = _domain
    EXACT_IDENTITIES['v1/ResourceQuota/' + _namespace + '/platform-budget'] = _domain
    EXACT_IDENTITIES['v1/LimitRange/' + _namespace + '/defaults'] = _domain
for _namespace, _name, _domain in (
    ('atlas-monitoring', 'monitoring-ingress', 'observability'),
    ('atlas-storage', 's3-default-deny', 'storage'),
    ('atlas-storage', 's3-clients', 'storage'),
    ('workload-web', 's3-client-egress', 'storage'),
):
    EXACT_IDENTITIES[f'networking.k8s.io/v1/NetworkPolicy/{_namespace}/{_name}'] = _domain


def stages():
    """A mixed rollback is planned, not an automatic response to a failure.

    Each state has one predecessor. No skipping directly to the final result.
    Pending/failed attempts cannot be promoted by a later successful observation.
    """
    result = []
    owners = {d: OLD for d in DOMAINS}
    apps = {OLD: 'strict'}

    def add(name, outcome, active=None, gate_b=False):
        result.append({'name': name, 'previous': result[-1]['name'] if result else None,
                       'outcome': outcome, 'activeOwner': active, 'owners': dict(owners),
                       'applications': dict(apps), 'atlasGate': gate_b})

    add('BASELINE_ADOPTED', 'success', OLD, True)
    apps.clear()
    add('SOURCE_RELEASED', 'released')

    def forward(prefix, domains):
        for domain in domains:
            owner = domain + '-foundation'
            apps[owner] = 'strict'
            add(f'{prefix}_{domain.upper()}_STRICT_REFUSED', 'blocked', owner)
            apps[owner] = 'window'
            owners[domain] = owner
            add(f'{prefix}_{domain.upper()}_ADOPTED_WINDOW', 'success', owner)
            apps[owner] = 'strict'
            add(f'{prefix}_{domain.upper()}_STRICT_RESTORED', 'success', owner)

    def reverse(prefix):
        apps.clear()
        add(prefix + '_TARGETS_RELEASED', 'released')
        apps[OLD] = 'strict'
        add(prefix + '_SOURCE_STRICT_REFUSED', 'blocked', OLD)
        apps[OLD] = 'window'
        owners.update({d: OLD for d in DOMAINS})
        add(prefix + '_SOURCE_READOPTED_WINDOW', 'success', OLD)
        apps[OLD] = 'strict'
        add(prefix + '_SOURCE_STRICT_RESTORED', 'success', OLD)
        add(prefix + '_VERIFIED', 'success', gate_b=True)

    forward('MIXED', DOMAINS[:2])
    reverse('MIXED_ROLLBACK')
    apps.clear()
    add('SECOND_SOURCE_RELEASED', 'released')
    forward('FORWARD', DOMAINS)
    add('FORWARD_VERIFIED', 'success', gate_b=True)
    reverse('REVERSE')
    return result


def check_sources():
    old_bytes = git_file(OLD_SHA, OLD_PATH)
    new_bytes = {d: git_file(NEW_SHA, p) for d, p in NEW_PATHS.items()}
    old = read_list(old_bytes)
    domains = {d: read_list(b) for d, b in new_bytes.items()}
    inventory = scope(old, domains)
    # This ceremony is frozen to OLD_SHA/NEW_SHA and the fixtures below. Current
    # installation templates may evolve (including namespace identities); they
    # are checked by platform:check, not evidence for this historical ceremony.
    # Four cross-namespace Roles/Bindings belong to secrets-controller, not the split.
    controller = 'gitops/platform/management/sealed-secrets/overlays/development/rendered.yaml'
    require(git_file(OLD_SHA, controller) == git_file(NEW_SHA, controller), 'controller payload changed')
    expected = {'oldLayoutCommit': OLD_SHA, 'newLayoutCommit': NEW_SHA,
                'sourceFilesSHA256': {OLD_PATH: hashlib.sha256(old_bytes).hexdigest(),
                                     **{NEW_PATHS[d]: hashlib.sha256(new_bytes[d]).hexdigest() for d in DOMAINS}},
                'controllerPayloadSHA256': hashlib.sha256(git_file(NEW_SHA, controller)).hexdigest(),
                'objects': inventory}
    require(json.loads((BASE / 'inventory.json').read_text()) == expected, 'inventory differs from immutable Git sources')
    for name, items in [('source', old), *domains.items()]:
        actual = read_list((BASE / 'fixtures' / name / 'resources.json').read_bytes())
        require(index(actual) == index(items), 'fixture differs from reviewed Git: ' + name)
        kustomization = json.loads((BASE / 'fixtures' / name / 'kustomization.yaml').read_text())
        require(kustomization == {'apiVersion': 'kustomize.config.k8s.io/v1beta1',
                'kind': 'Kustomization', 'resources': ['resources.json']},
                'fixture Kustomization adds inputs or transformations: ' + name)
    require(json.loads((BASE / 'stages.json').read_text()) == stages(), 'stage graph changed')
    return {'result': 'OFFLINE_SCOPE_VERIFIED', 'liveProof': False, 'clusterOperations': 0,
            'objects': len(inventory), 'domainCounts': dict(zip(DOMAINS, COUNTS)),
            'stages': len(stages()), 'inventorySHA256': digest(expected),
            'stagesSHA256': digest(stages()), 'checkoutHEAD': subprocess.check_output(
                ['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip(),
            'worktreeDirty': bool(subprocess.check_output(['git', 'status', '--porcelain'], cwd=ROOT))}


if __name__ == '__main__':
    print(json.dumps(check_sources(), indent=2))
