"""Offline contract checks. Synthetic observations are never runtime evidence."""
import copy
import json
from pathlib import Path
import unittest
from unittest.mock import patch

import contract as c
from transition import mode_patch, options, REPOSITORY, REVISION


class ScopeTests(unittest.TestCase):
    def setUp(self):
        self.old = c.read_list((c.BASE / 'fixtures/source/resources.json').read_bytes())
        self.domains = {d: c.read_list((c.BASE / 'fixtures' / d / 'resources.json').read_bytes()) for d in c.DOMAINS}

    def test_immutable_sources_and_frozen_fixtures(self):
        self.assertEqual(c.check_sources()['result'], 'OFFLINE_SCOPE_VERIFIED')

    def test_current_installation_templates_are_not_historical_evidence(self):
        current = {c.ROOT / p for p in c.NEW_PATHS.values()}
        current.add(c.ROOT / 'gitops/platform/management/sealed-secrets/overlays/development/rendered.yaml')
        original = Path.read_bytes

        def read_bytes(path):
            self.assertNotIn(path, current, 'current template used as frozen ceremony input')
            return original(path)

        with patch.object(Path, 'read_bytes', read_bytes):
            self.assertEqual(c.check_sources()['result'], 'OFFLINE_SCOPE_VERIFIED')

    def test_content_identity_scope_and_owner_negative_cases(self):
        cases = []
        x = copy.deepcopy(self.domains); x['storage'][-1]['spec']['egress'][0]['ports'][0]['port'] = 80; cases.append(x)
        x = copy.deepcopy(self.domains); x['secrets'][0]['metadata']['labels']['atlas.local/owner'] = 'other'; cases.append(x)
        x = copy.deepcopy(self.domains); x['secrets'][0]['metadata']['finalizers'] = ['unexpected']; cases.append(x)
        x = copy.deepcopy(self.domains); x['secrets'][0]['metadata']['ownerReferences'] = [{'uid':'other'}]; cases.append(x)
        x = copy.deepcopy(self.domains); x['secrets'][0]['metadata']['annotations'][c.TRACKING] = 'forged'; cases.append(x)
        x = copy.deepcopy(self.domains); x['secrets'][0]['kind'] = 'PersistentVolumeClaim'; cases.append(x)
        x = copy.deepcopy(self.domains); x['storage'].append(copy.deepcopy(x['storage'][0])); cases.append(x)
        x = copy.deepcopy(self.domains); x['secrets'][0], x['observability'][0] = x['observability'][0], x['secrets'][0]; cases.append(x)
        x = copy.deepcopy(self.domains); x['secrets'][0]['metadata']['namespace'] = 'atlas-secrets'; cases.append(x)
        for i, domains in enumerate(cases):
            with self.subTest(i=i), self.assertRaises(ValueError):
                c.scope(self.old, domains)

    def test_semantics_preserve_policy_and_nontracking_metadata(self):
        obj = copy.deepcopy(self.domains['storage'][-1])
        api = copy.deepcopy(obj)
        api['metadata'].update(uid='uid', resourceVersion='2', managedFields=[{'manager':'controller'}], generation=2)
        api['metadata']['annotations'] = {c.TRACKING:'owner'}
        api['status'] = {'used':{'pods':'9'}}
        self.assertEqual(c.semantic(api), c.semantic(obj))
        api['metadata']['annotations']['security-policy'] = 'changed'
        self.assertNotEqual(c.semantic(api), c.semantic(obj))

    def test_phase_graph_partial_rollback_and_single_window(self):
        phases = c.stages()
        self.assertEqual(len(phases), len({p['name'] for p in phases}))
        for before, after in zip(phases, phases[1:]):
            self.assertEqual(after['previous'], before['name'])
        for p in phases:
            self.assertLessEqual(list(p['applications'].values()).count('window'), 1)
            if p['atlasGate']:
                self.assertNotIn('window', p['applications'].values())
            if p['outcome'] == 'released':
                self.assertFalse(p['applications'])
            if p['outcome'] == 'blocked':
                self.assertEqual(p['applications'][p['activeOwner']], 'strict')
        mixed = next(p for p in phases if p['name'] == 'MIXED_OBSERVABILITY_STRICT_RESTORED')
        self.assertEqual(mixed['owners'], {'secrets':'secrets-foundation', 'observability':'observability-foundation', 'storage':c.OLD})
        for name in ['MIXED_ROLLBACK_VERIFIED', 'REVERSE_VERIFIED']:
            p = next(p for p in phases if p['name'] == name)
            self.assertEqual(set(p['owners'].values()), {c.OLD})
            self.assertTrue(p['atlasGate'])


class TransitionTests(unittest.TestCase):
    def observation(self, strict):
        sha = 'a'*40
        spec = {'project':'platform-project', 'source':{'repoURL':REPOSITORY, 'targetRevision':REVISION,
                'path':'gitops/platform/foundation/secrets/overlays/development'},
                'destination':{'server':'https://kubernetes.default.svc', 'namespace':'atlas-secrets'},
                'syncPolicy':{'syncOptions':options(strict)}}
        expected = {'apiVersion':'argoproj.io/v1alpha1', 'kind':'Application',
                    'metadata':{'name':'secrets-foundation', 'namespace':'argocd'}, 'spec':spec}
        current = copy.deepcopy(expected)
        current['metadata'].update(uid='app-uid', resourceVersion='123')
        current['status'] = {'sync':{'revision':sha,'status':'Synced'}, 'health':{'status':'Healthy'},
            'conditions':[{'type':'SharedResourceWarning'}] if strict else [],
            'operationState':{'phase':'Failed' if strict else 'Succeeded', 'finishedAt':'2026-09-27T00:00:00Z',
              'message':'Shared resource found: /Namespace/atlas-secrets' if strict else 'sync succeeded',
              'syncResult':{'revision':sha}, 'operation':{
                'info':[{'name':'ot1-stage','value':'previous'}],
                'sync':{'revision':sha, 'prune':False, 'syncOptions':options(strict)}}}}
        return current, expected, sha

    def test_both_directions_only_replace_options_after_concurrency_tests(self):
        for strict in (False, True):
            current, expected, sha = self.observation(not strict)
            patch = mode_patch(current, expected, sha, 'previous', strict)
            self.assertEqual([p['op'] for p in patch], ['test','test','test','replace'])
            self.assertEqual(patch[-1]['path'], '/spec/syncPolicy/syncOptions')
            self.assertEqual(patch[-1]['value'], options(strict))
            self.assertEqual(patch[2]['value'], current['spec'])

    def test_fail_closed_preconditions(self):
        base, expected, sha = self.observation(True)
        cases = []
        x=copy.deepcopy(base); x['metadata']['finalizers']=['resources-finalizer.argocd.argoproj.io']; cases.append(x)
        x=copy.deepcopy(base); x['metadata']['resourceVersion']=''; cases.append(x)
        x=copy.deepcopy(base); x['operation']={'sync':{}}; cases.append(x)
        x=copy.deepcopy(base); x['status']['sync']['revision']='b'*40; cases.append(x)
        x=copy.deepcopy(base); x['status']['operationState']['syncResult']['revision']='b'*40; cases.append(x)
        x=copy.deepcopy(base); x['status']['operationState']['operation']['info'][0]['value']='older'; cases.append(x)
        x=copy.deepcopy(base); x['status']['operationState']['message']='permission denied'; cases.append(x)
        x=copy.deepcopy(base); x['status']['conditions'].append({'type':'ComparisonError'}); cases.append(x)
        x=copy.deepcopy(base); x['status']['operationState']['operation']['sync']['prune']=True; cases.append(x)
        x=copy.deepcopy(base); x['spec']['source']['targetRevision']='codex/development-platform'; cases.append(x)
        x=copy.deepcopy(base); x['spec']['syncPolicy']['automated']={'enabled':True}; cases.append(x)
        for i,x in enumerate(cases):
            with self.subTest(i=i), self.assertRaises(ValueError):
                mode_patch(x,expected,sha,'previous',False)


if __name__ == '__main__':
    unittest.main()
