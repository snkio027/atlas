"""Transport for the finite isolation recipe. Every call has a target/deadline fence.

Construction alone is inert. The reviewed outer run must bind config, source
hashes, plan hash and a fresh Journal; this module offers no resume command.
"""
import datetime
import hashlib
import json
from pathlib import Path
import subprocess
import time

from isolation import digest, require


class Kubectl:
    def __init__(self, config):
        require(set(config) == {'kubectl', 'kubectlSHA256', 'kubeconfig', 'kubeconfigSHA256', 'context', 'clusterUID', 'deadline', 'namespace', 'serviceNamespace', 'serviceName'} and all(isinstance(v, str) and v for v in config.values()), 'invalid target configuration')
        self.config = dict(config)
        self.deadline = datetime.datetime.fromisoformat(config['deadline'])
        require(self.deadline.tzinfo is not None, 'deadline requires timezone')
        for field in ['kubectl', 'kubeconfig']:
            path = Path(config[field])
            require(path.is_absolute() and not path.is_symlink() and path.is_file(), 'unsafe tool/config path')
            require(hashlib.sha256(path.read_bytes()).hexdigest() == config[field+'SHA256'], field+' digest differs')
        self.base = [config['kubectl'], '--kubeconfig', config['kubeconfig'], '--context', config['context'], '--request-timeout=20s']

    def _call(self, args, body=None, timeout=25):
        remaining = (self.deadline - datetime.datetime.now(datetime.timezone.utc)).total_seconds()
        require(remaining > 0, 'execution deadline expired')
        p = subprocess.run(self.base + args, input=None if body is None else json.dumps(body).encode(), capture_output=True, timeout=min(timeout, remaining))
        return p

    def fence(self):
        require(hashlib.sha256(Path(self.config['kubeconfig']).read_bytes()).hexdigest() == self.config['kubeconfigSHA256'], 'kubeconfig changed')
        p = self._call(['get', 'namespace', 'kube-system', '-o', 'json'])
        require(p.returncode == 0, 'cluster identity read unavailable')
        require(json.loads(p.stdout)['metadata']['uid'] == self.config['clusterUID'], 'cluster UID differs')

    def get(self, kind, namespace=None, name=None):
        self.fence()
        args = ['get', kind] + ([name] if name else []) + (['-n', namespace] if namespace else []) + ['-o', 'json']
        p = self._call(args)
        require(p.returncode == 0, 'read unavailable: '+kind)
        return json.loads(p.stdout)

    def snapshot(self):
        c = self.config
        objects = []
        for ns in [c['namespace'], c['serviceNamespace']]:
            objects.append(self.get('namespace', name=ns))
            objects.extend(self.get('networkpolicies', ns)['items'])
        service = self.get('service', c['serviceNamespace'], c['serviceName'])
        ports = [p for p in service['spec']['ports'] if p['port'] == 8333 and p.get('protocol', 'TCP') == 'TCP']
        require(len(ports) == 1 and not service['spec'].get('externalName'), 'unexpected Service port/type')
        candidates = [p for p in self.get('pods', c['serviceNamespace'])['items'] if
                      all(p['metadata'].get('labels', {}).get(k) == v for k, v in service['spec']['selector'].items())]
        require(len(candidates) == 1, 'finite recipe expects one S3 backend')
        backend = candidates[0]
        require(not backend['metadata'].get('deletionTimestamp') and any(v['type'] == 'Ready' and v['status'] == 'True' for v in backend.get('status', {}).get('conditions', [])), 'S3 backend not Ready')
        port = ports[0]['targetPort']
        target_ports = [p['containerPort'] for container in backend['spec']['containers'] for p in container.get('ports', []) if p.get('protocol', 'TCP') == 'TCP' and (p.get('name') == port or p['containerPort'] == port)]
        require(target_ports == [8333], 'Service target port differs')
        slices = [s for s in self.get('endpointslices', c['serviceNamespace'])['items'] if s['metadata'].get('labels', {}).get('kubernetes.io/service-name') == c['serviceName']]
        ready = [e for s in slices for e in s['endpoints'] if e.get('conditions', {}).get('ready') is True]
        require(len(ready) == 1 and ready[0].get('targetRef', {}).get('uid') == backend['metadata']['uid'] and ready[0]['addresses'] == [backend['status']['podIP']], 'Service endpoint identity differs')
        # Additional backend labels may select additive policies. Include them
        # all rather than evaluating only the Service's narrower selector.
        labels = dict(backend['metadata'].get('labels', {}))
        return {'objects': objects, 'destination': {'namespace': c['serviceNamespace'], 'labels': labels}, 'port': 8333,
                'ip': service['spec']['clusterIP'], 'serviceUID': service['metadata']['uid'], 'backendUID': backend['metadata']['uid']}

    def create(self, obj):
        self.fence()
        p = self._call(['create', '-f', '-', '-o', 'json'], obj)
        require(p.returncode == 0, 'create failed/unknown; do not retry')
        live = json.loads(p.stdout)
        require(live['metadata']['labels'] == obj['metadata']['labels'] and live['spec']['automountServiceAccountToken'] is False, 'admitted probe identity differs')
        require(not any(live['spec'].get(k) for k in ['hostNetwork', 'hostPID', 'hostIPC', 'initContainers', 'ephemeralContainers', 'volumes']), 'admission added probe authority')
        containers = live['spec']['containers']
        require(len(containers) == 1 and containers[0]['image'] == obj['spec']['containers'][0]['image'] and not any(containers[0].get(k) for k in ['env', 'envFrom', 'volumeMounts']), 'admission changed probe executable/credentials')
        ref = {key: live['metadata'][key] for key in ['namespace', 'name', 'uid']}
        ref['labels'] = live['metadata']['labels']
        ref['specSHA256'] = digest({k: v for k, v in live['spec'].items() if k != 'nodeName'})
        return ref

    def _same(self, ref):
        live = self.get('pod', ref['namespace'], ref['name'])
        require(live['metadata']['uid'] == ref['uid'] and live['metadata'].get('labels', {}) == ref['labels'], 'probe UID/authorization labels changed')
        require(digest({k: v for k, v in live['spec'].items() if k != 'nodeName'}) == ref['specSHA256'], 'probe spec changed')
        return live

    def wait_ready(self, ref):
        end = time.monotonic() + 120
        while True:
            live = self._same(ref)
            require(live.get('status', {}).get('phase') not in ['Failed', 'Succeeded'], 'probe terminated')
            if any(c['type'] == 'Ready' and c['status'] == 'True' for c in live.get('status', {}).get('conditions', [])):
                return
            require(time.monotonic() < end, 'probe readiness timeout')
            time.sleep(2)

    def execute(self, ref, args):
        self._same(ref)
        p = self._call(['exec', '-n', ref['namespace'], ref['name'], '-c', 'probe', '--'] + args, timeout=20)
        self._same(ref)
        require(len(p.stdout) + len(p.stderr) <= 16384, 'unexpected probe output size')
        return {'exitCode': p.returncode, 'stdout': p.stdout.decode(), 'stderr': p.stderr.decode()}

    def secret_denial(self):
        self.fence()
        ns = self.config['namespace']
        args = ['auth', 'can-i', 'get', 'secrets/seaweedfs-auth', '-n', self.config['serviceNamespace'],
                '--as=system:serviceaccount:'+ns+':default', '--as-group=system:serviceaccounts',
                '--as-group=system:serviceaccounts:'+ns, '--as-group=system:authenticated']
        p = self._call(args)
        return {'exitCode': p.returncode, 'stdout': p.stdout.decode(), 'stderr': p.stderr.decode()}

    def delete_exact(self, ref):
        live = self._same(ref)
        uri = '/api/v1/namespaces/'+ref['namespace']+'/pods/'+ref['name']
        body = {'apiVersion': 'v1', 'kind': 'DeleteOptions', 'preconditions': {'uid': ref['uid'], 'resourceVersion': live['metadata']['resourceVersion']}, 'propagationPolicy': 'Background'}
        p = self._call(['delete', '--raw', uri, '-f', '-'], body)
        require(p.returncode == 0, 'delete failed/unknown; do not retry')

    def wait_absent(self, ref):
        end = time.monotonic() + 120
        while True:
            objects = self.get('pods', ref['namespace'])['items']
            same = [p for p in objects if p['metadata']['name'] == ref['name']]
            if not same:
                return
            require(len(same) == 1 and same[0]['metadata']['uid'] == ref['uid'], 'cleanup identity changed')
            require(time.monotonic() < end, 'cleanup timeout')
            time.sleep(2)
