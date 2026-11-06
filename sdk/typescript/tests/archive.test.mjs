import test from 'node:test';
import assert from 'node:assert/strict';
import {Client} from '../dist/index.js';

test('archive wait distinguishes durable receipts and terminal failure',async()=>{
 for(const state of ['stored','dead','deleted']){
  const client=new Client('http://local.test','',async()=>new Response(JSON.stringify({id:7,state}),{status:200}));
  if(state==='stored')assert.equal((await client.waitStorageJob('t',7)).state,'stored');
  else await assert.rejects(client.waitStorageJob('t',7));
 }
});

test('content upload preserves binary/Unicode and explicit retry identity',async()=>{
  const calls=[];const client=new Client('http://local.test','credential',async(url,init)=>{calls.push({url,init});return new Response(JSON.stringify(url.includes('storage-jobs')?{jobs:[],next_after:0,has_more:false}:{artifact_id:'image',version_id:'v1'}),{status:200});});
  const content=new Uint8Array([0,1,127,128,255]);await client.uploadContent('task/x',{artifact_id:'image',version_id:'v1',filename:'截图.png',content_type:'image/png'},content);
  assert.equal(calls[0].url,'http://local.test/v1/tasks/task%2Fx/contents');const body=JSON.parse(calls[0].init.body);
  assert.equal(body.filename,'截图.png');assert.equal(body.version_id,'v1');assert.deepEqual(Buffer.from(body.content_base64,'base64'),Buffer.from(content));
  assert.equal(calls[0].init.headers.Authorization,'Bearer credential');
  await client.listStorageJobs('task/x',42);assert.ok(calls[1].url.endsWith('storage-jobs?after=42'));
  await client.getStorageJob('task/x',7);await client.retryStorageJob('task/x',7);assert.equal(calls[3].init.method,'POST');
  assert.throws(()=>client.uploadContent('t',{artifact_id:'a',version_id:'v',filename:'x',content_type:'x'},new Uint8Array(8*1024*1024+1)));
});
