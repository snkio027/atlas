import copy
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from evidence import Journal
from isolation import ContractError, classify, effective, pods, preflight, run

ROOT = Path(__file__).resolve().parents[2]
NS = 'project-platform-smoke'
TARGET = {'namespace': 'service-object-storage', 'labels': {'app.kubernetes.io/name': 'seaweedfs'}}


def fixture():
    objects = []
    for name in ['gitops/platform/foundation/overlays/development/resources.json',
                 'gitops/platform/foundation/storage/overlays/development/resources.json']:
        objects.extend(json.loads((ROOT/name).read_text())['items'])
    for obj in objects:
        if obj['kind'] == 'Namespace':
            obj['metadata'].setdefault('labels', {})['kubernetes.io/metadata.name'] = obj['metadata']['name']
    web = next(o for o in json.loads((ROOT/'gitops/workloads/web-smoke/overlays/development/resources.json').read_text())['items'] if o['kind'] == 'Deployment')
    image = web['spec']['template']['spec']['containers'][0]['image']
    positive, negative = pods(image, NS, 'atlas-s3-check')
    return objects, positive, negative, web


def response(reachable, ip='10.96.0.10'):
    return {'exitCode': 1, 'stdout': '', 'stderr': f'Connecting to {ip}:8333 ({ip}:8333)\n' + ('  HTTP/1.1 403 Forbidden\nwget: server returned error: HTTP/1.1 403 Forbidden\n' if reachable else 'wget: download timed out\n')}


class API:
    def __init__(self, objects):
        self.objects = objects
        self.effects = []
        self.commands = []
        self.reads = 0
        self.failure = None

    def snapshot(self):
        self.reads += 1
        return {'objects': self.objects, 'destination': TARGET, 'port': 8333, 'ip': '10.96.0.10', 'serviceUID': 'service-uid', 'backendUID': 'backend-uid'}

    def create(self, obj):
        self.effects.append(('create', obj['metadata']['name']))
        if self.failure == 'unknown-create':
            raise TimeoutError('request outcome unknown')
        return {'namespace': NS, 'name': obj['metadata']['name'], 'uid': obj['metadata']['name'] + '-uid'}

    def wait_ready(self, ref):
        if self.failure == 'not-ready':
            raise TimeoutError('readiness deadline')

    def execute(self, ref, command):
        self.effects.append(('execute', ref['name']))
        self.commands.append(command)
        reachable = not ref['name'].endswith('-unbound')
        if self.failure == 'positive-timeout':
            reachable = False
        if self.failure == 'negative-http':
            reachable = True
        return response(reachable)

    def secret_denial(self):
        return {'exitCode': 0 if self.failure == 'secret-allowed' else 1, 'stdout': 'yes\n' if self.failure == 'secret-allowed' else 'no\n', 'stderr': ''}

    def delete_exact(self, ref):
        self.effects.append(('delete', ref['name']))
        if self.failure == 'cleanup':
            raise OSError('unknown delete result')

    def wait_absent(self, ref):
        pass


class IsolationContract(unittest.TestCase):
    def setUp(self):
        self.objects, self.positive, self.negative, self.web = fixture()

    def plan(self):
        return preflight(self.objects, self.positive, self.negative, TARGET)

    def test_current_render_and_actual_web_identity(self):
        self.plan()
        web = {'namespace': NS, 'labels': self.web['spec']['template']['metadata']['labels']}
        for direction in ['Ingress', 'Egress']:
            self.assertFalse(effective(self.objects, web, TARGET, 8333, direction))
        wrong = copy.deepcopy(self.positive)
        wrong['metadata']['labels'] = web['labels']
        with self.assertRaisesRegex(ContractError, 'no S3 authorization'):
            preflight(self.objects, wrong, self.negative, TARGET)

    def test_missing_one_side_is_not_connectivity(self):
        for name in ['s3-client-egress', 's3-clients']:
            with self.subTest(name=name):
                objects = [o for o in self.objects if o['metadata']['name'] != name]
                with self.assertRaisesRegex(ContractError, 'positive peer denied'):
                    preflight(objects, self.positive, self.negative, TARGET)

    def test_additive_policy_and_wrong_namespace_fail_closed(self):
        for direction in ['Ingress', 'Egress']:
            objects = copy.deepcopy(self.objects)
            objects.append({'kind': 'NetworkPolicy', 'metadata': {'namespace': NS if direction == 'Egress' else TARGET['namespace'], 'name': 'unintended-allow'}, 'spec': {'podSelector': {}, 'policyTypes': [direction], direction.lower(): [{}]}})
            with self.assertRaisesRegex(ContractError, 'negative peer allowed'):
                preflight(objects, self.positive, self.negative, TARGET)
        wrong = copy.deepcopy(TARGET)
        wrong['namespace'] = NS
        with self.assertRaises(ContractError):
            preflight(self.objects, self.positive, self.negative, wrong)

    def test_no_credentials_or_unsupported_selector(self):
        for key, value in [('volumes', [{'secret': {'secretName': 'bad'}}]), ('automountServiceAccountToken', True), ('hostNetwork', True)]:
            a, b = copy.deepcopy(self.positive), copy.deepcopy(self.negative)
            a['spec'][key] = b['spec'][key] = value
            with self.assertRaises(ContractError):
                preflight(self.objects, a, b, TARGET)
        objects = copy.deepcopy(self.objects)
        next(o for o in objects if o['metadata']['name'] == 's3-client-egress')['spec']['podSelector']['matchExpressions'] = []
        with self.assertRaisesRegex(ContractError, 'unsupported selector'):
            preflight(objects, self.positive, self.negative, TARGET)

    def test_api_empty_omission_is_the_same_contract(self):
        planned = self.plan()
        for o in self.objects:
            if o['kind'] == 'NetworkPolicy' and o['metadata']['name'] == 'default-deny':
                o['spec']['ingress'] = []; o['spec']['egress'] = []
        self.assertEqual(planned.sha256, self.plan().sha256)

    def test_http_rejection_is_reachability_not_network_denial(self):
        self.assertEqual(classify(response(True), '10.96.0.10', 8333, True)['httpStatus'], 403)
        with self.assertRaises(ContractError):
            classify(response(True), '10.96.0.10', 8333, False)
        for text in ['bad address', 'permission denied', 'connection refused', 'network is unreachable', 'wget: not found']:
            result = response(False); result['stderr'] += text
            with self.assertRaises(ContractError):
                classify(result, '10.96.0.10', 8333, False)
        for code in [0, 124, 137, -1]:
            result = response(False); result['exitCode'] = code
            with self.assertRaises(ContractError):
                classify(result, '10.96.0.10', 8333, False)

    def test_real_entrypoint_claims_before_effect_and_commits_last(self):
        api = API(self.objects)
        with tempfile.TemporaryDirectory() as tmp:
            journal = Journal(Path(tmp)/'attempt')
            original = api.create
            def create(obj):
                role = 'negative' if obj['metadata']['name'].endswith('-unbound') else 'positive'
                self.assertTrue((journal.directory/(role+'-create-intent.json')).exists())
                return original(obj)
            api.create = create
            run(self.plan(), api, journal)
            self.assertEqual([x[0] for x in api.effects], ['create', 'create', 'execute', 'execute', 'execute', 'delete', 'delete'])
            self.assertEqual(api.commands, [api.commands[0]]*3)
            self.assertTrue((journal.directory/'final.json').exists())
            with self.assertRaises(FileExistsError):
                run(self.plan(), api, journal)
            self.assertEqual(len(api.effects), 7)

    def test_invalid_positive_fails_before_any_effect(self):
        planned = self.plan()
        api = API([o for o in self.objects if o['metadata']['name'] != 's3-client-egress'])
        with tempfile.TemporaryDirectory() as tmp:
            with self.assertRaises(ContractError):
                run(planned, api, Journal(Path(tmp)/'attempt'))
        self.assertEqual(api.effects, [])

    def test_all_failure_boundaries_stop_without_replay_or_pass(self):
        for failure in ['unknown-create', 'not-ready', 'positive-timeout', 'negative-http', 'secret-allowed', 'cleanup']:
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as tmp:
                api = API(self.objects); api.failure = failure
                journal = Journal(Path(tmp)/'attempt')
                with self.assertRaises((ContractError, TimeoutError, OSError)):
                    run(self.plan(), api, journal)
                self.assertFalse((journal.directory/'final.json').exists())
                effects = list(api.effects)
                with self.assertRaises(FileExistsError):
                    run(self.plan(), api, journal)
                self.assertEqual(api.effects, effects)

    def test_attempt_directory_sync_failure_is_not_reusable(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp)/'attempt'
            with patch('evidence.os.fsync', side_effect=OSError('parent sync failed')):
                with self.assertRaises(OSError):Journal(path)
            self.assertTrue(path.is_dir())
            with self.assertRaises(FileExistsError):Journal(path)

    def test_durable_claim_failure_has_zero_effects(self):
        with tempfile.TemporaryDirectory() as tmp:
            api = API(self.objects); journal = Journal(Path(tmp)/'attempt')
            with patch('evidence.os.fsync', side_effect=OSError('disk sync failed')):
                with self.assertRaises(OSError):
                    run(self.plan(), api, journal)
            self.assertEqual(api.effects, [])
            with self.assertRaises(FileExistsError):
                run(self.plan(), api, journal)
            self.assertEqual(api.effects, [])


if __name__ == '__main__':
    unittest.main()
