import test from 'node:test';
import assert from 'node:assert/strict';
import {Client} from '../dist/index.js';

test('refund retries preserve authentication, minor units and idempotency; statement is HTML',async()=>{
  const seen=[];
  const client=new Client('http://billing.test','test-token',async(url,init)=>{
    seen.push({url,init});
    assert.equal(init.headers.Authorization,'Bearer test-token');
    return new Response(url.endsWith('/statement')?'<html>receipt</html>':'{"refunds":[]}');
  });
  await client.requestRefund('inv/1','pay1',35,'unused','stable-key');
  await client.requestRefund('inv/1','pay1',35,'unused','stable-key');
  assert.equal(seen[0].url,'http://billing.test/v1/billing/invoices/inv%2F1/refunds');
  assert.equal(seen[0].init.headers['Idempotency-Key'],'stable-key');
  assert.equal(JSON.parse(seen[0].init.body).amount_minor,35);
  assert.equal(seen[0].init.body,seen[1].init.body);
  assert.equal(await client.invoiceStatement('inv/1'),'<html>receipt</html>');
});
