#!/usr/bin/env python3
"""Offline cloud deployment contracts and production preflight rejection cases."""
import copy
import importlib.util
import json
from pathlib import Path
import subprocess
import unittest
import yaml

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('preflight', ROOT/'scripts/preflight-helm.py')
preflight = importlib.util.module_from_spec(spec)
spec.loader.exec_module(preflight)
CATALOG = json.loads((ROOT/'deploy/clouds/catalog.json').read_text())
capacity_spec = importlib.util.spec_from_file_location('capacity', ROOT/'scripts/check-local-capacity.py')
capacity = importlib.util.module_from_spec(capacity_spec)
capacity_spec.loader.exec_module(capacity)

def values(cloud):
    merged = {}
    for path in ['deploy/helm/agent-continuity/values.yaml', 'deploy/environments/production/values.yaml', cloud['values']]:
        preflight.merge(merged, yaml.safe_load((ROOT/path).read_text()))
    return merged

class CloudContracts(unittest.TestCase):
    def test_local_capacity_guard(self):
        nodes=[{'metadata':{'name':'local'},'status':{'allocatable':{'pods':'3'},'conditions':[{'type':'Ready','status':'True'}]}}]
        running={'spec':{'nodeName':'local'},'status':{'phase':'Running'}}
        self.assertEqual(capacity.check(nodes,[running]),2)
        with self.assertRaises(ValueError): capacity.check(nodes,[running,running])
        with self.assertRaises(ValueError): capacity.check(nodes,[{'spec':{},'status':{'phase':'Pending'}}])
        self.assertEqual(capacity.check(nodes,[{'spec':{'nodeName':'local'},'status':{'phase':'Succeeded'}}]),3)

    def test_render_and_external_dependencies(self):
        for cloud in CATALOG['clouds']:
            with self.subTest(cloud=cloud['id']):
                rendered = subprocess.check_output(['helm','template','agent-continuity',str(ROOT/'deploy/helm/agent-continuity'),'-f',str(ROOT/'deploy/environments/production/values.yaml'),'-f',str(ROOT/cloud['values'])], text=True)
                docs = [x for x in yaml.safe_load_all(rendered) if x]
                self.assertFalse(any(x['kind']=='StatefulSet' for x in docs), 'no dev DB/broker in a cloud profile')
                pods = [x['spec']['template']['spec'] for x in docs if x['kind']=='Deployment']
                claim = values(cloud)['artifactStorage']['existingClaim']
                self.assertEqual(len(pods), 2)
                for pod in pods:
                    self.assertTrue(any(v.get('persistentVolumeClaim',{}).get('claimName')==claim for v in pod['volumes']))
                self.assertTrue(any(x['kind']=='CronJob' for x in docs))
                self.assertTrue(any(x['kind']=='Ingress' and x['spec']['tls'] for x in docs))

    def test_placeholders_and_dangerous_overrides_rejected(self):
        for cloud in CATALOG['clouds']:
            v = values(cloud)
            with self.assertRaises(ValueError): preflight.validate_production_values(v)
            preflight.merge(v, {'networkPolicy':{'databasePeers':[{'ipBlock':{'cidr':'10.42.0.8/32'}}]},'image':{'repository':'registry.company.test/continuity','tag':'0.3.0'},'ingress':{'host':'continuity.company.test','tls':[{'secretName':'continuity-ingress-tls','hosts':['continuity.company.test']}]}})
            preflight.validate_production_values(v)
            for override in [{'postgresql':{'enabled':True}}, {'payments':{'demoEnabled':True}}, {'workflows':{'demoEnabled':True}}, {'kms':{'config':{'simulation':True}}}, {'networkPolicy':{'enabled':False}}, {'archive':{'broker':{'install':True}}}, {'backup':{'existingPVC':''}}, {'ingress':{'tls':[]}}, {'autoscaling':{'minReplicas':1}}]:
                bad = preflight.merge(copy.deepcopy(v), override)
                with self.assertRaises(ValueError): preflight.validate_production_values(bad)

if __name__=='__main__': unittest.main(verbosity=2)
