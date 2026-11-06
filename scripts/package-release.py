#!/usr/bin/env python3
"""Build an auditable source/chart/client delivery bundle; excludes local secrets.

Includes six desktop OS/architecture clients unless --host-only is selected.
Container images are built/pulled by deployment; this is not an air-gapped bundle.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import tarfile
import tempfile

ROOT = Path(__file__).resolve().parents[1]
TOP = {'adapter','api','cmd','deploy','docs','integrations','internal','migrations','pkg','plugins','scripts','sdk','tests','.github'}
FILES = {'go.mod','go.sum','Makefile','Dockerfile','.dockerignore','.gitignore','README.md','LICENSE','.env.example'}

def run(args, **kw):
    return subprocess.run(args, cwd=ROOT, check=True, **kw)

def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--version', default='0.3.0-local')
    p.add_argument('--host-only', action='store_true')
    a = p.parse_args()
    if not all(c.isalnum() or c in '.-_' for c in a.version):
        raise ValueError('Invalid version')
    dist = ROOT/'dist'
    dist.mkdir(exist_ok=True)
    output = dist/('agent-continuity-'+a.version+'.tar.gz')
    if output.exists():
        raise ValueError('Version already packaged; select a new version')
    names = run(['git','ls-files','--cached','--others','--exclude-standard','-z'], capture_output=True).stdout.decode().split('\0')
    with tempfile.TemporaryDirectory(prefix='continuity-release-') as folder:
        stage = Path(folder)/('agent-continuity-'+a.version)
        stage.mkdir()
        for name in sorted(set(names)):
            rel = Path(name)
            if not name or (rel.parts[0] not in TOP and name not in FILES): continue
            source = ROOT/rel
            if source.is_symlink() or not source.is_file(): continue
            if any(part in {'node_modules','__pycache__','.build','.gradle','obj','bin'} for part in rel.parts): continue
            if rel.suffix in {'.pem','.key','.p12','.pfx','.dump','.vsix'} or '.continuity-install' in rel.parts: continue
            target = stage/rel
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(source,target)
        # Fixed dependency archives are already vendored; no repository refresh.
        vsix=ROOT/'integrations/codebuddy-extension/agent-continuity-codebuddy.vsix'
        if not vsix.is_file(): raise ValueError('Build CodeBuddy VSIX with scripts/package-codebuddy-extension.sh first')
        shutil.copy2(vsix,stage/'integrations/codebuddy-extension'/vsix.name)
        charts=stage/'charts'
        charts.mkdir()
        for chart in ['agent-continuity','agent-continuity-infra']:
            run(['helm','lint',str(stage/'deploy/helm'/chart)], stdout=subprocess.DEVNULL)
            run(['helm','package',str(stage/'deploy/helm'/chart),'--destination',str(charts)], stdout=subprocess.DEVNULL)
        targets=[('darwin','amd64'),('darwin','arm64'),('linux','amd64'),('linux','arm64'),('windows','amd64'),('windows','arm64')]
        if a.host_only:
            targets=[({'Darwin':'darwin','Linux':'linux','Windows':'windows'}[platform.system()], 'arm64' if platform.machine() in ('arm64','aarch64') else 'amd64')]
        for goos,arch in targets:
            for binary in ['agentcloud','daemon']:
                destination=stage/'clients'/(goos+'-'+arch)/(binary+('.exe' if goos=='windows' else ''))
                destination.parent.mkdir(parents=True,exist_ok=True)
                print('Building',goos,arch,binary,flush=True)
                run(['go','build','-p','2','-trimpath','-o',str(destination),'./cmd/'+binary],env={**os.environ,'CGO_ENABLED':'0','GOMAXPROCS':'2','GOOS':goos,'GOARCH':arch})
        metadata={'version':a.version,'go_toolchain':run(['go','version'],capture_output=True,text=True).stdout.strip(),'git_commit':run(['git','rev-parse','HEAD'],capture_output=True,text=True).stdout.strip(),'includes_worktree_changes':bool(run(['git','status','--porcelain'],capture_output=True).stdout),'client_targets':targets,'air_gapped':False,'required':['Docker or Kubernetes + Helm','namespace secrets','Python 3 for installer/preflight','external merchant credentials only for live payments']}
        (stage/'RELEASE.json').write_text(json.dumps(metadata,indent=2)+'\n')
        checks=[]
        for path in sorted(stage.rglob('*')):
            if path.is_file(): checks.append(hashlib.sha256(path.read_bytes()).hexdigest()+'  '+path.relative_to(stage).as_posix())
        (stage/'SHA256SUMS').write_text('\n'.join(checks)+'\n')
        with tarfile.open(output,'w:gz') as archive: archive.add(stage,arcname=stage.name)
    checksum=hashlib.sha256(output.read_bytes()).hexdigest()
    output.with_suffix(output.suffix+'.sha256').write_text(checksum+'  '+output.name+'\n')
    print(str(output),flush=True)

if __name__=='__main__': main()
