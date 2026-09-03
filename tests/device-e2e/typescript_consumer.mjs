import assert from "node:assert/strict";
import {Client, DeviceRuntime, generateDeviceIdentity, MemoryStore} from "../../sdk/typescript/dist/index.js";

const baseURL = process.env.CONTINUITY_E2E_URL;
const token = process.env.CONTINUITY_E2E_TOKEN;
const accountID = process.env.CONTINUITY_E2E_ACCOUNT_ID;
const taskID = process.env.CONTINUITY_E2E_TASK_ID;
const client = new Client(baseURL, token);
const identity = await generateDeviceIdentity("TypeScript tablet");
const runtime = new DeviceRuntime(client, identity, accountID, new MemoryStore());
await runtime.register();

const first = await runtime.syncOnce();
assert.ok(first.pulled >= 1);
assert.ok((await runtime.receivedEvents()).some(event => event.task_id === taskID && event.payload?.text === "来自 Python 设备的上下文"));

const agent = await client.createAgent({name: "CodeBuddy on tablet", provider: "codebuddy", model: "codebuddy", capabilities: ["capture", "rehydrate"]});
await client.bindTaskAgent(taskID, {agent_id: String(agent.agent_id), resume_mode: "rehydrate", capabilities: ["capture", "rehydrate"]});
const event = await runtime.queueEvent(taskID, "agent.message.created", {text: "TypeScript 设备已接续任务", progress: 70}, {agentID: String(agent.agent_id), providerID: "codebuddy"});
const second = await runtime.syncOnce();
assert.equal(second.pushed, 1);
console.log(JSON.stringify({task_id: taskID, event_id: event.event_id, device_id: identity.device_id, agent_id: agent.agent_id}));
