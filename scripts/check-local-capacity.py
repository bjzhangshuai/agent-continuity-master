#!/usr/bin/env python3
"""Read-only Pod-slot guard for the shared local cluster. Never evicts workloads."""
import argparse
import json
import subprocess

def check(nodes, pods, minimum=2):
    healthy = {n['metadata']['name']: int(n['status']['allocatable']['pods']) for n in nodes
               if not n.get('spec', {}).get('unschedulable')
               and any(c['type']=='Ready' and c['status']=='True' for c in n['status']['conditions'])
               and not any(c['type'] in ('MemoryPressure','DiskPressure','PIDPressure') and c['status']=='True' for c in n['status']['conditions'])}
    pending = 0
    for p in pods:
        if p.get('status', {}).get('phase') in ('Succeeded','Failed'): continue
        node = p.get('spec', {}).get('nodeName')
        if node in healthy: healthy[node] -= 1
        elif not node and p.get('status', {}).get('phase')=='Pending': pending += 1
    free = sum(max(0, count) for count in healthy.values())
    if free < minimum or pending:
        raise ValueError(f'Local cluster has {free} free Pod slots and {pending} unscheduled Pending pods; need {minimum} slots and no waiting backlog. No workloads changed.')
    return free

def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--context', default='rancher-desktop')
    args=p.parse_args()
    def get(*tail):
        return json.loads(subprocess.check_output(['kubectl','--context',args.context,*tail,'-o','json'],timeout=20))['items']
    free=check(get('get','nodes'),get('get','pods','-A'))
    print(f'PASS: {free} local Pod slots available; capacity is necessary, not a scheduling guarantee.')

if __name__=='__main__':
    try: main()
    except ValueError as e: raise SystemExit(f'DEPLOYMENT BLOCKED: {e}')
