import assert from "node:assert/strict";
import test from "node:test";

import {buildSignedEvent, Client, DeviceRuntime, generateDeviceIdentity, MemoryStore, portableEventSigningBytes} from "../dist/index.js";

test("QR, task permission, and SSO contracts", async () => {
  const requests = [];
  const fetcher = async (url, init) => {
    requests.push({url, init});
    if (url.endsWith("/permissions")) {
      return new Response(JSON.stringify({permissions: [{task_id: "task_1", account_id: "acct_2", permission: "read", created_at: "2026-01-01T00:00:00Z"}]}), {status: 200});
    }
    if (init.method === "PUT") {
      return new Response(JSON.stringify({task_id: "task_1", account_id: "acct_2", permission: "write", created_at: "2026-01-01T00:00:00Z"}), {status: 200});
    }
    if (url.endsWith("/qr-invitations")) {
      return new Response(JSON.stringify({qr_content: "agentcontinuity://invite?token=invite", qr_svg: "<svg/>"}), {status: 200});
    }
    if (url.endsWith("/auth/sso/exchange")) {
      return new Response(JSON.stringify({access_token: "access", refresh_token: "refresh"}), {status: 200});
    }
    return new Response(null, {status: 204});
  };
  const client = new Client("https://continuity.example", "token", fetcher);

  const qr = await client.createQRInvitation({version: 1, invitation_id: "invite_1", task_id: "task_1", inviter_identity: "acct_1", inviter_device_id: "device_1", permission_scope: "read", expiration: "2026-01-01T00:00:00Z", nonce: "nonce", token_digest: "digest", signature: "signature", token: "invite"});
  assert.equal(qr.qr_svg, "<svg/>");
  assert.equal((await client.listTaskPermissions("task_1"))[0].permission, "read");
  assert.equal((await client.putTaskPermission("task_1", "acct_2", "write")).permission, "write");
  await client.deleteTaskPermission("task_1", "acct_2");
  assert.equal((await client.exchangeSSO("oidc-token")).access_token, "access");

  assert.deepEqual(requests.map(({init}) => init.method), ["POST", "GET", "PUT", "DELETE", "POST"]);
  assert.equal(JSON.parse(requests.at(-1).init.body).id_token, "oidc-token");
});

test("portable signature and offline queue", async () => {
  const identity = await generateDeviceIdentity("TypeScript");
  const event = await buildSignedEvent({identity, accountID: "acct_1", taskID: "task_1", eventType: "progress.updated", payload: {text: "你好 <world>"}, sequence: 1});
  const decode = value => Buffer.from(value.replaceAll("-", "+").replaceAll("_", "/"), "base64");
  const publicKey = await crypto.subtle.importKey("raw", decode(identity.signing_public_key), {name: "Ed25519"}, false, ["verify"]);
  assert.equal(await crypto.subtle.verify("Ed25519", publicKey, decode(event.signature.slice(4)), portableEventSigningBytes(event)), true);
  const runtime = new DeviceRuntime(new Client("http://127.0.0.1:1"), identity, "acct_1", new MemoryStore());
  await runtime.queueEvent("task_1", "progress.updated", {percent: 25});
  assert.equal((await runtime.pendingEvents()).length, 1);
});
