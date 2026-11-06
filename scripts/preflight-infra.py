#!/usr/bin/env python3
"""Read-only infrastructure checks; never print generated manifests or Secret values."""
import argparse
import json
from pathlib import Path
import subprocess
import yaml
from importlib.util import module_from_spec, spec_from_file_location

ROOT = Path(__file__).resolve().parents[1]

def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--context', required=True)
    p.add_argument('--namespace', required=True)
    p.add_argument('--release', default='agent-continuity-infra')
    p.add_argument('-f', '--values', action='append', default=[])
    a = p.parse_args()
    spec = spec_from_file_location('app_preflight', ROOT/'scripts/preflight-helm.py')
    helper = module_from_spec(spec)
    spec.loader.exec_module(helper)
    chart = ROOT/'deploy/helm/agent-continuity-infra'
    v = yaml.safe_load((chart/'values.yaml').read_text())
    flags = []
    for file in a.values:
        helper.merge(v, yaml.safe_load(Path(file).read_text()) or {})
        flags += ['-f', file]
    def command(argv):
        r = subprocess.run(argv, capture_output=True, text=True, timeout=45)
        if r.returncode: raise ValueError(f'{argv[0]} failed; check context/RBAC/config (details suppressed)')
        return r.stdout
    def get(kind, name):
        return json.loads(command(['kubectl','--context',a.context,'-n',a.namespace,'get',kind,name,'--ignore-not-found','-o','json']) or '{}')
    docs = [d for d in yaml.safe_load_all(command(['helm','template',a.release,str(chart),'-n',a.namespace,*flags])) if d]
    generated = {(d['kind'],d['metadata']['name']):d for d in docs}
    ns = get('namespace', a.namespace)
    if not ns: raise ValueError('Create the intended namespace first')
    nodes = json.loads(command(['kubectl','--context',a.context,'get','nodes','-o','json']))['items']
    healthy = [n for n in nodes if not n.get('spec',{}).get('unschedulable') and any(c['type']=='Ready' and c['status']=='True' for c in n['status']['conditions']) and not any(c['type'] in ('MemoryPressure','DiskPressure','PIDPressure') and c['status']=='True' for c in n['status']['conditions'])]
    if not healthy: raise ValueError('No schedulable Ready node without pressure')
    if a.context == 'rancher-desktop':
        capacity_spec = spec_from_file_location('capacity', ROOT/'scripts/check-local-capacity.py')
        capacity = module_from_spec(capacity_spec)
        capacity_spec.loader.exec_module(capacity)
        pods = json.loads(command(['kubectl','--context',a.context,'get','pods','-A','-o','json']))['items']
        capacity.check(nodes, pods)
    if v['nats']['enabled'] and len(healthy)<int(v['nats']['config']['cluster']['replicas']):
        raise ValueError('NATS anti-affinity requires one healthy node per replica; use the dev infra profile locally')
    for name, crd in [('keda','scaledobjects.keda.sh'),('monitoring','prometheuses.monitoring.coreos.com')]:
        if not v[name]['enabled']: continue
        existing = get('crd', crd)
        if existing:
            owner = existing['metadata'].get('annotations',{})
            if owner.get('meta.helm.sh/release-name')!=a.release or owner.get('meta.helm.sh/release-namespace')!=a.namespace:
                raise ValueError(f'{name} controller/CRD already exists outside this release; disable its dependency and reuse it')
    if v['monitoring']['enabled'] and ns['metadata'].get('labels',{}).get('continuity.agent-continuity.io/monitor')!='true':
        raise ValueError('Label the app/infra namespace continuity.agent-continuity.io/monitor=true for scoped ServiceMonitor discovery')
    def secret(name, key=None):
        value = get('secret', name) or generated.get(('Secret',name),{})
        if not value or (key and not value.get('data',{}).get(key)):
            raise ValueError(f'Secret {name} missing required key {key or "(Secret missing)"}')
    if v['minio']['enabled']:
        for name, keys in [(v['minio']['existingSecret'],['rootUser','rootPassword']),
                           (v['minio']['tls']['certSecret'],['public.crt','private.key','ca.crt']),
                           (v['minioBootstrap']['clientSecret'],['access-key','secret-key','ca.crt'])]:
            for key in keys: secret(name,key)
    for d in docs:
        pod = d.get('spec',{}).get('template',{}).get('spec',{})
        for c in pod.get('containers',[]) + pod.get('initContainers',[]):
            for env in c.get('env',[]):
                ref = env.get('valueFrom',{}).get('secretKeyRef')
                if ref and not ref.get('optional'): secret(ref['name'], ref['key'])
        for volume in pod.get('volumes',[]):
            if 'secret' in volume:
                ref=volume['secret']
                secret(ref['secretName'])
                for item in ref.get('items',[]): secret(ref['secretName'],item['key'])
            claim=volume.get('persistentVolumeClaim',{}).get('claimName')
            if claim and ('PersistentVolumeClaim',claim) not in generated:
                if get('pvc',claim).get('status',{}).get('phase')!='Bound': raise ValueError(f'Existing PVC {claim} is not Bound')
    print('PASS: infrastructure render, namespace, node pressure/replicas, controller ownership, Secret references and existing PVCs. No writes.')

if __name__=='__main__':
    try: main()
    except (ValueError, KeyError, subprocess.TimeoutExpired) as e: raise SystemExit(f'INFRA PREFLIGHT BLOCKED: {e}')
