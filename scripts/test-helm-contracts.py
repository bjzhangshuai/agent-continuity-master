#!/usr/bin/env python3
"""Offline Helm contracts; no cluster mutation and no Secret values printed."""
from pathlib import Path
import subprocess
import hashlib
import yaml
# Kubernetes YAML accepts a bare '=' enum value in Prometheus CRDs as a string.
yaml.SafeLoader.add_constructor('tag:yaml.org,2002:value', lambda loader, node: loader.construct_scalar(node))
ROOT=Path(__file__).resolve().parents[1]
CHART=ROOT/'deploy/helm/agent-continuity'
def render(profile, *extra):
    cmd=['helm','template','agent-continuity',str(CHART),'-n','contract-test','-f',str(ROOT/f'deploy/environments/{profile}/values.yaml'),*extra]
    return list(yaml.safe_load_all(subprocess.check_output(cmd,text=True)))
for profile in ('dev','staging','production'):
    docs=render(profile)
    by_name={(d['kind'],d['metadata']['name']):d for d in docs if d}
    api=by_name['Deployment','agent-continuity']['spec']['template']
    worker=by_name['Deployment','agent-continuity-archive-worker']['spec']['template']
    assert api['metadata']['annotations']['checksum/config']
    assert worker['metadata']['annotations']['checksum/secrets']
    assert worker['spec']['containers'][0]['readinessProbe']['httpGet']['path']=='/ready'
    assert ('NetworkPolicy','agent-continuity-archive-worker') in by_name
    if profile!='dev':
        assert ('StatefulSet','agent-continuity-archive-broker') not in by_name
        assert ('ServiceMonitor','agent-continuity-archive-worker') in by_name
        assert api['spec']['volumes'][-2]['persistentVolumeClaim']['claimName']=='continuity-staging-rwx'
    changed=render(profile,'--set-string','secretRevision=rotated')
    changed_api=next(d for d in changed if d and d['kind']=='Deployment' and d['metadata']['name']=='agent-continuity')
    assert changed_api['spec']['template']['metadata']['annotations']['checksum/secrets']!=api['metadata']['annotations']['checksum/secrets']
    print(profile+': configuration, secret rollout, workers, network and monitoring PASS')
bad=subprocess.run(['helm','template','test',str(CHART),'-f',str(ROOT/'deploy/environments/dev/values.yaml'),'--set','archive.workerReplicas=2'],capture_output=True)
assert bad.returncode!=0,'multi-worker RWO was accepted'
keda=render('production','--set','archive.autoscaling.enabled=false','--set','archive.queueAutoscaling.enabled=true','--set','archive.queueAutoscaling.serverAddress=https://prometheus.example.internal')
scaled=next(d for d in keda if d and d['kind']=='ScaledObject')
assert scaled['spec']['triggers'][0]['metadata']['query'].startswith('max(')
infra=list(yaml.safe_load_all(subprocess.check_output(['helm','template','agent-continuity-infra',str(ROOT/'deploy/helm/agent-continuity-infra')],text=True)))
sts=next(d for d in infra if d and d['kind']=='StatefulSet')
assert sts['spec']['replicas']==3 and sts['spec']['volumeClaimTemplates']
assert sts['spec']['template']['spec']['affinity']['podAntiAffinity']['requiredDuringSchedulingIgnoredDuringExecution']
cfg=next(d for d in infra if d and d['kind']=='ConfigMap')['data']['nats.conf']
assert 'CONTINUITY_NATS_TOKEN' in cfg and 'jetstream' in cfg and 'tls' in cfg
print('official NATS chart: 3 replicas, durable PVC, TLS/auth, anti-affinity PASS')

INFRA=ROOT/'deploy/helm/agent-continuity-infra'
for line in (INFRA/'charts.sha256').read_text().splitlines():
    checksum, filename = line.split()
    assert hashlib.sha256((INFRA/'charts'/filename).read_bytes()).hexdigest()==checksum,filename+' checksum mismatch'
def render_infra(*extra):
    return [d for d in yaml.safe_load_all(subprocess.check_output(['helm','template','agent-continuity-infra',str(INFRA),'-n','contract-test','--include-crds',*extra],text=True)) if d]
dev=render_infra('-f',str(ROOT/'deploy/environments/dev/infra-values.yaml'))
assert not any(d['kind'] in ('CustomResourceDefinition','ClusterRole','ClusterRoleBinding','StatefulSet') for d in dev)
dep=next(d for d in dev if d['kind']=='Deployment')
assert dep['metadata']['name']=='continuity-minio' and dep['spec']['replicas']==1
pod=dep['spec']['template']['spec']
assert pod['automountServiceAccountToken'] is False
c=pod['containers'][0]
assert c['readinessProbe']['httpGet']['scheme']=='HTTPS'
assert c['startupProbe'] and c['livenessProbe'] and c['resources']['limits']['memory']=='512Mi'
assert c['securityContext']['readOnlyRootFilesystem']
pvc=next(d for d in dev if d['kind']=='PersistentVolumeClaim')
assert pvc['spec']['resources']['requests']['storage']=='2Gi'
assert pvc['metadata']['annotations']['helm.sh/resource-policy']=='keep'
job=next(d for d in dev if d['kind']=='Job')
args=job['spec']['template']['spec']['containers'][0]['args'][0]
assert 'anonymous set none' in args and 'quota set' in args
assert '--insecure' not in args and 'console123' not in args
test_pod=next(d for d in dev if d['kind']=='Pod' and d['metadata'].get('annotations',{}).get('helm.sh/hook')=='test')
test_script=test_pod['spec']['containers'][0]['args'][0]
assert 'trap - EXIT' in test_script and 'test "$attempt" -lt 15' in test_script
policy=next(d for d in dev if d['kind']=='ConfigMap' and d['metadata']['name'].endswith('minio-policy'))['data']['policy.json']
assert 's3:*' not in policy and 's3:DeleteBucket' not in policy and 's3:ListAllMyBuckets' not in policy
assert 'continuity-archive/continuity/*' in policy
full=render_infra('--set','monitoring.enabled=true','--set','keda.enabled=true','--set','otel.enabled=true','--set','tempo.enabled=true')
for kind in ('Prometheus','Deployment','StatefulSet','CustomResourceDefinition'):
    assert any(d['kind']==kind for d in full),kind
crds={d['metadata']['name'] for d in full if d['kind']=='CustomResourceDefinition'}
assert {'scaledobjects.keda.sh','servicemonitors.monitoring.coreos.com'}<=crds
prom=next(d for d in full if d['kind']=='Prometheus')['spec']
assert prom['serviceMonitorSelector']['matchLabels']['app.kubernetes.io/part-of']=='agent-continuity'
assert prom['retention']=='24h'
assert any(d['metadata']['name']=='continuity-otel' for d in full)
assert any(d['metadata']['name']=='continuity-tempo' for d in full)
print('MinIO: TLS, probes, non-root, retained PVC, private quota bucket, scoped credentials PASS')
print('optional Prometheus/Grafana + KEDA CRDs + OTel/Tempo: full render PASS; dev installs none of these')
