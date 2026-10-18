import copy
import datetime
import hashlib
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch
from isolation import ContractError, digest
from kubectl_adapter import Kubectl
from test_isolation import fixture, NS, TARGET


class AdapterContract(unittest.TestCase):
    def test_snapshot_binds_service_endpoint_and_all_backend_labels(self):
        objects, _, _, _ = fixture()
        api = object.__new__(Kubectl)
        api.config = {'namespace': NS, 'serviceNamespace': TARGET['namespace'], 'serviceName': 'seaweedfs'}
        service = {'metadata': {'uid': 'svc'}, 'spec': {'selector': TARGET['labels'], 'ports': [{'port': 8333, 'targetPort': 's3'}], 'clusterIP': '10.96.0.10'}}
        backend = {'metadata': {'uid': 'pod', 'labels': dict(TARGET['labels'], extra='identity')}, 'spec': {'containers': [{'ports': [{'name': 's3', 'containerPort': 8333}]}]}, 'status': {'podIP': '10.244.0.1', 'conditions': [{'type': 'Ready', 'status': 'True'}]}}
        endpoint = {'metadata': {'labels': {'kubernetes.io/service-name': 'seaweedfs'}}, 'endpoints': [{'conditions': {'ready': True}, 'targetRef': {'uid': 'pod'}, 'addresses': ['10.244.0.1']}]}
        def get(kind, ns=None, name=None):
            if kind == 'namespace':return next(o for o in objects if o['kind'] == 'Namespace' and o['metadata']['name'] == name)
            if kind == 'networkpolicies':return {'items': [o for o in objects if o['kind'] == 'NetworkPolicy' and o['metadata']['namespace'] == ns]}
            if kind == 'service':return service
            if kind == 'pods':return {'items': [backend]}
            if kind == 'endpointslices':return {'items': [endpoint]}
            self.fail(kind)
        api.get = get
        result = api.snapshot()
        self.assertEqual(result['destination']['labels']['extra'], 'identity')
        endpoint['endpoints'][0]['targetRef']['uid'] = 'another'
        with self.assertRaisesRegex(ContractError, 'endpoint identity'):api.snapshot()

    def test_target_and_deadline_are_checked_before_effect(self):
        with tempfile.TemporaryDirectory() as tmp:
            tool = Path(tmp)/'kubectl';tool.write_text('test-tool')
            config = Path(tmp)/'config';config.write_text('test-config')
            conf = {'kubectl': str(tool), 'kubectlSHA256': hashlib.sha256(tool.read_bytes()).hexdigest(), 'kubeconfig': str(config), 'kubeconfigSHA256': hashlib.sha256(config.read_bytes()).hexdigest(), 'context': 'kind-reviewed', 'clusterUID': 'approved', 'deadline': (datetime.datetime.now(datetime.timezone.utc)+datetime.timedelta(minutes=1)).isoformat(), 'namespace': NS, 'serviceNamespace': TARGET['namespace'], 'serviceName': 'seaweedfs'}
            api = Kubectl(conf)
            completed = subprocess.CompletedProcess([], 0, json.dumps({'metadata': {'uid': 'wrong'}}).encode(), b'')
            with patch('kubectl_adapter.subprocess.run', return_value=completed) as call:
                with self.assertRaisesRegex(ContractError, 'cluster UID'):api.create({})
                self.assertEqual(call.call_count, 1)
                self.assertNotIn('create', call.call_args.args[0])
            api.deadline = datetime.datetime.now(datetime.timezone.utc)-datetime.timedelta(seconds=1)
            with patch('kubectl_adapter.subprocess.run') as call:
                with self.assertRaisesRegex(ContractError, 'deadline'):api.create({})
                call.assert_not_called()

    def test_probe_spec_and_labels_are_fenced_after_admission(self):
        api = object.__new__(Kubectl)
        live = {'metadata': {'uid': 'uid', 'labels': {'atlas.local/s3-client': 'true'}}, 'spec': {'containers': [{'image': 'locked'}], 'nodeName': 'assigned'}}
        ref = {'namespace': NS, 'name': 'probe', 'uid': 'uid', 'labels': dict(live['metadata']['labels']), 'specSHA256': digest({'containers': [{'image': 'locked'}]})}
        api.get = lambda *args: live
        self.assertEqual(api._same(ref), live)
        live['spec']['containers'][0]['image'] = 'different'
        with self.assertRaisesRegex(ContractError, 'spec changed'):api._same(ref)
        live['spec']['containers'][0]['image'] = 'locked'
        live['metadata']['labels'] = {}
        with self.assertRaisesRegex(ContractError, 'labels changed'):api._same(ref)

    def test_secret_denial_queries_the_workload_identity_without_reading_secret(self):
        api = object.__new__(Kubectl);api.config = {'namespace': NS, 'serviceNamespace': TARGET['namespace']}
        api.fence = lambda: None
        commands = []
        def call(args):
            commands.append(args)
            return subprocess.CompletedProcess(args, 1, b'no\n', b'')
        api._call = call
        self.assertEqual(api.secret_denial(), {'exitCode': 1, 'stdout': 'no\n', 'stderr': ''})
        self.assertEqual(commands[0][:4], ['auth', 'can-i', 'get', 'secrets/seaweedfs-auth'])
        self.assertIn('--as=system:serviceaccount:'+NS+':default', commands[0])
        self.assertEqual(sum(arg.startswith('--as-group=') for arg in commands[0]), 3)

    def test_execute_selects_explicit_container_and_checks_uid_twice(self):
        api = object.__new__(Kubectl);reads=[];commands=[]
        ref = {'namespace': NS, 'name': 'probe', 'uid': 'uid'}
        api._same = lambda value: reads.append(copy.deepcopy(value))
        def call(args, body=None, timeout=25):
            commands.append(args)
            return subprocess.CompletedProcess(args, 1, b'', b'expected')
        api._call = call
        result = api.execute(ref, ['wget', '-S'])
        self.assertEqual(reads, [ref, ref])
        self.assertEqual(commands, [['exec', '-n', NS, 'probe', '-c', 'probe', '--', 'wget', '-S']])
        self.assertEqual(result, {'exitCode': 1, 'stdout': '', 'stderr': 'expected'})


if __name__ == '__main__':unittest.main()
