#!/usr/bin/env python3
"""Run workflow HTTP E2E against a dedicated PostgreSQL database, never a cluster rollout.
Requires CONTINUITY_WORKFLOW_TEST_DATABASE_URL with a continuity_test_* DB name.
Binds only 127.0.0.1:18081; stops its own process and preserves the test DB as evidence.
"""
import base64,json,os,secrets,socket,subprocess,time,urllib.request
from pathlib import Path
from urllib.parse import urlparse
ROOT=Path(__file__).resolve().parents[1]

def main():
    dsn=os.environ.get('CONTINUITY_WORKFLOW_TEST_DATABASE_URL','')
    if not urlparse(dsn).path.startswith(('/continuity_test_','/continuity_archive_test_')):
        raise SystemExit('Refusing non-dedicated database')
    with socket.socket() as sock:
        try: sock.bind(('127.0.0.1',18081))
        except OSError: raise SystemExit('Port 18081 occupied; not touching its owner')
    state=ROOT/'.data/workflow-e2e';state.mkdir(parents=True,exist_ok=True)
    env={**os.environ,'GOMAXPROCS':'2','CONTINUITY_STORE':'postgres','CONTINUITY_DATABASE_URL':dsn,
        'CONTINUITY_LISTEN_ADDRESS':'127.0.0.1:18081','CONTINUITY_AUTO_MIGRATE':'true',
        'CONTINUITY_ARTIFACT_BACKEND':'local','CONTINUITY_ARTIFACT_DIR':str(state/'artifacts'),
        'CONTINUITY_JWT_SECRET':secrets.token_hex(32),'CONTINUITY_BOOTSTRAP_TOKEN':secrets.token_hex(32),
        'CONTINUITY_MASTER_KEY':os.environ.get('CONTINUITY_WORKFLOW_MASTER_KEY',base64.urlsafe_b64encode(secrets.token_bytes(32)).decode().rstrip('=')),
        'CONTINUITY_PAYMENT_CONFIG':json.dumps({'demo_enabled':True,'allow_live':False,'public_base_url':'http://127.0.0.1:18081'}),
        'CONTINUITY_WORKFLOW_CONFIG':json.dumps({'demo_enabled':True,'allow_live':False}),
        'CONTINUITY_KMS_CONFIG':json.dumps({'current':'local-aws-v1','simulation':True,'keys':[{'id':'local-aws-v1','cloud':'aws','key_id':'simulated-key'}]})}
    # No inherited live provider/identity configuration may bleed into the fixture.
    for key in list(env):
        if key.startswith('CONTINUITY_') and (key.endswith('_FILE') or key.startswith(('CONTINUITY_ARCHIVE','CONTINUITY_OIDC','CONTINUITY_SCIM','CONTINUITY_OTLP','CONTINUITY_TREASURY','CONTINUITY_BILLING_PRICE'))): env.pop(key,None)
    binary=state/'server'
    subprocess.run(['go','build','-p','2','-o',str(binary),'./cmd/server'],cwd=ROOT,env=env,check=True)
    base='http://127.0.0.1:18081'
    def api(path,body=None,headers=None):
        req=urllib.request.Request(base+path,data=json.dumps(body).encode() if body is not None else None,headers={'Content-Type':'application/json',**(headers or {})})
        with urllib.request.urlopen(req,timeout=3) as r:return json.load(r)
    def start(log):
        proc=subprocess.Popen([str(binary),'serve'],cwd=ROOT,env=env,stdout=log,stderr=log)
        for _ in range(80):
            if proc.poll() is not None: raise RuntimeError('Fixture server failed; inspect .data/workflow-e2e/server.log')
            try: api('/v1/health/ready');return proc
            except Exception: time.sleep(.25)
        proc.terminate();proc.wait(timeout=10);raise RuntimeError('Fixture not ready')
    def stop(proc):
        if proc and proc.poll() is None:
            proc.terminate()
            try:proc.wait(timeout=10)
            except subprocess.TimeoutExpired:proc.kill();proc.wait(timeout=5)
    proc=None
    with (state/'server.log').open('w') as log:
        try:
            proc=start(log)
            auth={'X-Bootstrap-Token':env['CONTINUITY_BOOTSTRAP_TOKEN']}
            portal=os.environ.get('CONTINUITY_E2E_PORTAL_STATE','')
            merchant=json.loads(Path(portal).read_text())['users']['platform']['account_id'] if portal else api('/v1/accounts',{'name':'Isolated workflow merchant '+secrets.token_hex(4)},auth)['account_id']
            stop(proc);proc=None
            env['CONTINUITY_TREASURY_CONFIG']=json.dumps({'owner_account_id':merchant,'demo_enabled':True,'allow_live':False})
            admins=[merchant]
            portal=os.environ.get('CONTINUITY_E2E_PORTAL_STATE','')
            if portal:admins.append(json.loads(Path(portal).read_text())['users']['platform']['account_id'])
            env['CONTINUITY_PLATFORM_SUPER_ADMINS']=','.join(admins)
            proc=start(log)
            token=api('/v1/auth/token',{'account_id':merchant,'subject':'workflow-e2e','ttl_seconds':900},auth)['access_token']
            tests={**env,'CONTINUITY_E2E_URL':base,'CONTINUITY_E2E_BOOTSTRAP_TOKEN':env['CONTINUITY_BOOTSTRAP_TOKEN'],'CONTINUITY_E2E_MERCHANT_ACCOUNT':merchant,'CONTINUITY_E2E_TREASURY_TOKEN':token,'CONTINUITY_E2E_WORKFLOWS':'1'}
            pattern=os.environ.get('CONTINUITY_WORKFLOW_TEST_PATTERN','Test(WorkflowCommerceKMSJourney|StandardProtocolJourney)$')
            listed=subprocess.check_output(['go','test','-list',pattern,'./tests/e2e'],cwd=ROOT,env=tests,text=True)
            cases=[line for line in listed.splitlines() if line.startswith('Test')]
            if not cases:raise RuntimeError('No E2E cases matched')
            for i,name in enumerate(cases):
                # Keep production auth throttles intact; each case gets a new HTTP process.
                if i:stop(proc);proc=None;proc=start(log)
                subprocess.run(['go','test','-p','2','-count=1','-v','-timeout','180s','./tests/e2e','-run','^'+name+'$'],cwd=ROOT,env=tests,check=True)
            print('PASS:',len(cases),'isolated HTTP E2E journeys')
        finally:stop(proc)
if __name__=='__main__':main()
