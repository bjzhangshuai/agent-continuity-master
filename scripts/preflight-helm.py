#!/usr/bin/env python3
"""Read-only deployment preflight. Never print Secret values or raw manifests."""
import argparse
import base64
import json
import os
from pathlib import Path
import subprocess
from urllib.parse import urlparse
import yaml

ROOT = Path(__file__).resolve().parents[1]

def merge(a, b):
    for key, value in b.items():
        if isinstance(value, dict) and isinstance(a.get(key), dict):
            merge(a[key], value)
        else:
            a[key] = value
    return a

def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--context', required=True)
    p.add_argument('--namespace', required=True)
    p.add_argument('--release', default='agent-continuity')
    p.add_argument('-f', '--values', action='append', required=True)
    p.add_argument('--production', action='store_true')
    args = p.parse_args()
    chart = ROOT / 'deploy/helm/agent-continuity'
    v = yaml.safe_load((chart / 'values.yaml').read_text())
    values_args = []
    for name in args.values:
        merge(v, yaml.safe_load(Path(name).read_text()) or {})
        values_args += ['-f', name]
    def command(argv):
        result = subprocess.run(argv, capture_output=True, text=True, timeout=45)
        if result.returncode:
            # kubectl errors can contain endpoints/config; never echo raw stderr.
            raise ValueError(f'{argv[0]} failed: check context/RBAC/config (details suppressed)')
        return result.stdout
    def get(kind, name):
        return json.loads(command(['kubectl', '--context', args.context, '-n', args.namespace, 'get', kind, name, '-o', 'json']))
    docs = list(yaml.safe_load_all(command(['helm', 'template', args.release, str(chart), '-n', args.namespace, *values_args])))
    generated = {(d['kind'], d['metadata']['name']): d for d in docs if d}
    secrets = {}
    def secret(name, keys=()):
        if name not in secrets:
            secrets[name] = generated.get(('Secret', name)) or get('secret', name)
        data = secrets[name].get('data', {})
        for key in keys:
            if not data.get(key):
                raise ValueError(f'Secret {name} missing required key {key}')
        return data
    core = secret(v['existingSecret'], ['database-url', 'jwt-secret', 'bootstrap-token', 'master-key'])
    if v['databaseTLS']['existingSecret']: secret(v['databaseTLS']['existingSecret'], ['ca.crt'])
    for d in docs:
        if not d: continue
        kind = d['kind']
        if kind == 'ScaledObject':
            get('crd', 'scaledobjects.keda.sh')
            auth = v['archive']['queueAutoscaling']['authenticationRef']
            if auth: get('triggerauthentication', auth)
        if kind in ('ServiceMonitor', 'PrometheusRule'):
            get('crd', {'ServiceMonitor':'servicemonitors.monitoring.coreos.com', 'PrometheusRule':'prometheusrules.monitoring.coreos.com'}[kind])
        template = d.get('spec', {}).get('template', {})
        if kind == 'CronJob': template = d['spec']['jobTemplate']['spec']['template']
        pod = template.get('spec', {})
        for image_secret in pod.get('imagePullSecrets', []): secret(image_secret['name'])
        for c in pod.get('containers', []) + pod.get('initContainers', []):
            for env in c.get('env', []):
                ref = env.get('valueFrom', {}).get('secretKeyRef')
                if ref and not ref.get('optional'): secret(ref['name'], [ref['key']])
        for volume in pod.get('volumes', []):
            if 'secret' in volume: secret(volume['secret']['secretName'])
            claim = volume.get('persistentVolumeClaim', {}).get('claimName')
            if claim and ('PersistentVolumeClaim', claim) not in generated:
                pvc = get('pvc', claim)
                if pvc.get('status', {}).get('phase') != 'Bound': raise ValueError(f'PVC {claim} is not Bound')
                if claim == v['artifactStorage']['existingClaim'] and v['artifactStorage']['accessMode'] not in pvc['spec']['accessModes']:
                    raise ValueError(f'PVC {claim} access mode differs from values')
    if v['networkPolicy']['enabled'] and not v['postgresql']['enabled'] and not v['networkPolicy']['databasePeers']:
        raise ValueError('External PostgreSQL requires explicit networkPolicy.databasePeers on TCP/5432')
    if v['archive']['enabled']:
        archive_secret = v['archive']['existingSecret']
        config = json.loads(base64.b64decode(secret(archive_secret, ['config.json'])['config.json'])) if archive_secret else v['archive']['config']
        if archive_secret and not config.get('enabled'): raise ValueError('Archive Secret config.json must set enabled=true')
        mounts = {'/run/archive/': archive_secret, '/run/archive-storage/': v['archive']['storageSecret'], '/run/archive-broker/': v['archive']['broker']['existingSecret'] or args.release+'-archive-broker'}
        for obj in [config.get('broker', {}), *config.get('targets', [])]:
            for key, path in obj.items():
                if key.endswith('_file') and path:
                    prefix = next((prefix for prefix in mounts if path.startswith(prefix)), None)
                    if not prefix or '/' in path[len(prefix):] or not mounts[prefix]: raise ValueError(f'Unmapped credential file for {key}')
                    secret(mounts[prefix], [path[len(prefix):]])
        if args.production:
            broker = config.get('broker', {})
            if not broker.get('url', '').startswith('tls://') or broker.get('allow_insecure') or broker.get('replicas') not in (3, 5):
                raise ValueError('Production requires TLS broker and stream replicas=3 or 5')
            if v['archive']['broker']['install']: raise ValueError('Development broker is not production HA')
            if not v['networkPolicy']['brokerPeers']: raise ValueError('Production requires explicit brokerPeers')
            if not config.get('targets') or any(t.get('provider') == 'filesystem' for t in config['targets']): raise ValueError('Production requires real object storage targets')
            if any('.invalid' in t.get('endpoint', '') for t in config['targets']): raise ValueError('Replace placeholder cloud endpoints')
    if args.production:
        if v['image']['tag'] in ('dev', 'latest', ''): raise ValueError('Production requires an immutable release image tag')
        db = urlparse(base64.b64decode(core['database-url']).decode())
        if 'sslmode=verify-full' not in db.query: raise ValueError('Production external PostgreSQL must use sslmode=verify-full')
        if not v['backup']['enabled']: raise ValueError('Production backup.enabled must be true with a dedicated existingPVC')
    print('PASS: Helm rendered; referenced Secrets/keys, PVCs, CRDs and egress prerequisites checked. No cluster writes.')
    print('Still required: actual network/TLS connectivity, shared POSIX file locking, cloud probe and backup restore drill.')

if __name__ == '__main__':
    try: main()
    except (ValueError, KeyError, subprocess.TimeoutExpired) as error:
        raise SystemExit(f'PREFLIGHT BLOCKED: {error}')
