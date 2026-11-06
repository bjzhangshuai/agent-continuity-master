export const SDK_VERSION = "0.1.0";

export interface StorageJob {id:number;task_id:string;kind:"event"|"artifact";resource_id:string;version_id?:string;target_id:string;state:"queued"|"processing"|"retry"|"stored"|"dead"|"deleted";attempts:number;size:number;content_hash?:string;error_code?:string;operation?:"archive"|"delete";legal_hold?:boolean}
export interface StorageJobsPage {jobs:StorageJob[];next_after:number;has_more:boolean}

export interface Task {
  task_id: string;
  account_id: string;
  organization_id?: string;
  team_id?: string;
  title: string;
  objective: string;
  status: string;
  head_event_ids: string[];
  created_at: string;
  updated_at: string;
  [key: string]: unknown;
}

export interface UniversalEvent {
  event_id: string;
  task_id: string;
  branch_id: string;
  account_id: string;
  device_id: string;
  parent_event_ids: string[];
  logical_time: {wall_time_ms: number; counter: number; node: string};
  device_sequence: number;
  event_type: string;
  schema_version: string;
  content_type: string;
  content_digest: string;
  signature: string;
  idempotency_key: string;
  participant_id?: string;
  agent_id?: string;
  provider_id?: string;
  session_ref?: string;
  payload?: unknown;
  artifact_refs?: string[];
  created_at_client: string;
  extensions?: unknown;
  [key: string]: unknown;
}

export interface DeviceIdentity {
  device_id: string;
  name: string;
  signing_private_key: string;
  signing_public_key: string;
  encryption_private_key: string;
  encryption_public_key: string;
}

export interface SyncTaskState {
  task_id: string;
  head_event_ids: string[];
  event_cursor: number;
}

export interface SyncReport {
  pushed: number;
  duplicates: number;
  rejected: number;
  pulled: number;
  acknowledged: number;
}

export interface TaskPermission {
  task_id: string;
  account_id: string;
  permission: "read" | "write" | "admin";
  created_at: string;
}

export interface QRInvitation {
  version: 1;
  invitation_id: string;
  task_id: string;
  inviter_identity: string;
  inviter_device_id: string;
  permission_scope: "read" | "write";
  expiration: string;
  nonce: string;
  token_digest: string;
  key_exchange_material?: unknown;
  signature: string;
  token: string;
}

export interface ContinuityPackage {
  protocol_version: string;
  schema_version: string;
  capabilities: string[];
  target_agent: string;
  task: Task;
  events: UniversalEvent[];
  artifact_manifests: Record<string, unknown>[];
  branches?: Record<string, unknown>[];
  participants?: Record<string, unknown>[];
  agent_bindings?: Record<string, unknown>[];
  conflicts?: Record<string, unknown>[];
  snapshots?: Record<string, unknown>[];
  generated_at: string;
}

export interface BillingAccount {
  billing_account_id: string;
  account_id: string;
  organization_id?: string;
  plan: string;
  status: string;
  currency: string;
  limits: Record<string, unknown>;
  current_period_start: string;
  current_period_end: string;
}

export interface MarketplaceTransaction {
  transaction_id: string;
  listing_id: string;
  buyer_account_id: string;
  seller_account_id: string;
  buyer_agent_id?: string;
  seller_agent_id?: string;
  amount_minor: number;
  commission_minor: number;
  currency: string;
  status: "pending" | "authorized" | "completed" | "cancelled";
  [key: string]: unknown;
}

export class APIError extends Error {
  constructor(public readonly status: number, public readonly code: string, message: string) {
    super(message);
    this.name = "APIError";
  }
}

export class Client {

  /** 201 means accepted. Poll for state=stored. Persist/reuse version_id on retries. */
  uploadContent(taskID:string, input:{artifact_id:string;version_id:string;filename:string;content_type:string;parent_version_id?:string}, content:Uint8Array):Promise<Record<string,unknown>> {
    if(content.byteLength>8*1024*1024)throw new Error("Use resumable upload for content larger than 8 MiB");
    let binary="";for(let start=0;start<content.length;start+=8192)binary+=String.fromCharCode(...content.subarray(start,start+8192));
    return this.request("POST",`/v1/tasks/${encodeURIComponent(taskID)}/contents`,{...input,content_base64:btoa(binary)});
  }
  listStorageJobs(taskID:string,after=0):Promise<StorageJobsPage>{return this.request("GET",`/v1/tasks/${encodeURIComponent(taskID)}/storage-jobs?after=${after}`);}
  getStorageJob(taskID:string,id:number):Promise<StorageJob>{return this.request("GET",`/v1/tasks/${encodeURIComponent(taskID)}/storage-jobs/${id}`);}
  async waitStorageJob(taskID:string,id:number,timeoutMs=120000):Promise<StorageJob>{
    if(!Number.isFinite(timeoutMs)||timeoutMs<=0)throw new Error("Positive archive wait timeout required");
    const deadline=Date.now()+timeoutMs;
    while(Date.now()<deadline){const job=await this.getStorageJob(taskID,id);if(job.state==="stored")return job;if(job.state==="dead"||job.state==="deleted")throw new Error(`Archive job ${id}: ${job.state} ${job.error_code||""}`);await new Promise(resolve=>setTimeout(resolve,Math.min(1000,Math.max(0,deadline-Date.now()))));}
    throw new Error(`Archive job ${id}: receipt timeout (upload may still finish)`);
  }
  retryStorageJob(taskID:string,id:number):Promise<unknown>{return this.request("POST",`/v1/tasks/${encodeURIComponent(taskID)}/storage-jobs/${id}/retry`);}
  constructor(public readonly baseURL: string, public token = "", private readonly fetcher: typeof fetch = fetch) {}

  async request<T>(method: string, path: string, body?: unknown, extraHeaders: Record<string,string> = {}): Promise<T> {
    return JSON.parse((await this.requestText(method,path,body,extraHeaders)) || "null") as T;
  }
  private async requestText(method: string, path: string, body?: unknown, extraHeaders: Record<string,string> = {}): Promise<string> {
    const response = await this.fetcher(this.baseURL.replace(/\/$/, "") + path, {
      method,
      headers: {
        Accept: "application/json",
        ...extraHeaders,
        ...(this.token ? {Authorization: `Bearer ${this.token}`} : {}),
        ...(body === undefined ? {} : {"Content-Type": "application/json"}),
        "User-Agent": `agent-continuity-typescript/${SDK_VERSION}`,
      },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    const text = await response.text();
    if (!response.ok) {
      let value; try {value=JSON.parse(text);} catch {value=undefined;}
      throw new APIError(response.status, value?.error?.code ?? "http_error", value?.error?.message ?? response.statusText);
    }
    return text;
  }

  createRun(plan:Record<string,unknown>,key:string):Promise<Record<string,unknown>> {return this.request("POST","/v1/orchestration/runs",plan,{"Idempotency-Key":key});}
  getRun(id:string):Promise<Record<string,unknown>> {return this.request("GET",`/v1/orchestration/runs/${encodeURIComponent(id)}`);}
  controlRun(id:string,action:"approve"|"cancel"):Promise<Record<string,unknown>> {return this.request("POST",`/v1/orchestration/runs/${encodeURIComponent(id)}/${action}`);}
  issueFiscal(id:string,buyer:{buyer_name:string;tax_id:string}):Promise<Record<string,unknown>> {return this.request("POST",`/v1/billing/invoices/${encodeURIComponent(id)}/fiscal`,buyer);}
  getSettlement(id:string):Promise<Record<string,unknown>> {return this.request("GET",`/v1/commerce/settlements/${encodeURIComponent(id)}`);}
  listInvoices(): Promise<Record<string,unknown>> {return this.request("GET","/v1/billing/invoices?limit=200");}
  getInvoice(id:string): Promise<Record<string,unknown>> {return this.request("GET",`/v1/billing/invoices/${encodeURIComponent(id)}`);}
  quoteInvoice(period:{billing_account_id:string;period_start:string;period_end:string}): Promise<Record<string,unknown>> {return this.request("POST","/v1/billing/quote",period);}
  issueInvoice(period:{billing_account_id:string;period_start:string;period_end:string},key:string): Promise<Record<string,unknown>> {return this.request("POST","/v1/billing/invoices",period,{"Idempotency-Key":key});}
  createPayment(id:string,provider:string,key:string): Promise<Record<string,unknown>> {return this.request("POST",`/v1/billing/invoices/${encodeURIComponent(id)}/payments`,{provider},{"Idempotency-Key":key});}
  reconcilePayment(id:string,payment:string): Promise<Record<string,unknown>> {return this.request("POST",`/v1/billing/invoices/${encodeURIComponent(id)}/payments/${encodeURIComponent(payment)}/reconcile`);}
  /** Persist/reuse key across retries; approval remains a merchant operation. */
  requestRefund(id:string,payment:string,amountMinor:number,reason:string,key:string): Promise<Record<string,unknown>> {return this.request("POST",`/v1/billing/invoices/${encodeURIComponent(id)}/refunds`,{payment_id:payment,amount_minor:amountMinor,reason},{"Idempotency-Key":key});}
  reconcileRefund(id:string,refund:string): Promise<Record<string,unknown>> {return this.request("POST",`/v1/billing/invoices/${encodeURIComponent(id)}/refunds/${encodeURIComponent(refund)}/reconcile`);}
  invoiceStatement(id:string): Promise<string> {return this.requestText("GET",`/v1/billing/invoices/${encodeURIComponent(id)}/statement`);}

  health(): Promise<void> {
    return this.request("GET", "/v1/health/ready");
  }

  issueToken(bootstrapToken: string, accountID: string, subject: string): Promise<Record<string, unknown>> {
    return this.requestWithHeaders("POST", "/v1/auth/token", {account_id: accountID, subject, ttl_seconds: 3600}, {"X-Bootstrap-Token": bootstrapToken});
  }

  refreshToken(refreshToken: string): Promise<Record<string, unknown>> {
    return this.request("POST", "/v1/auth/refresh", {refresh_token: refreshToken});
  }

  logout(refreshToken: string): Promise<void> {
    return this.request("POST", "/v1/auth/logout", {refresh_token: refreshToken});
  }

  private async requestWithHeaders<T>(method: string, path: string, body: unknown, extraHeaders: Record<string, string>): Promise<T> {
    const response = await this.fetcher(this.baseURL.replace(/\/$/, "") + path, {
      method,
      headers: {Accept: "application/json", "Content-Type": "application/json", ...extraHeaders},
      body: JSON.stringify(body),
    });
    const text = await response.text();
    const value = text ? JSON.parse(text) : undefined;
    if (!response.ok) throw new APIError(response.status, value?.error?.code ?? "http_error", value?.error?.message ?? response.statusText);
    return value as T;
  }

  async listTasks(limit = 100): Promise<Task[]> {
    const value = await this.request<{tasks: Task[]}>("GET", `/v1/tasks?limit=${encodeURIComponent(limit)}`);
    return value.tasks;
  }

  createTask(input: {title: string; objective: string; origin_device?: string; organization_id?: string; team_id?: string; metadata?: unknown}): Promise<Task> {
    return this.request("POST", "/v1/tasks", input);
  }

  getTask(taskID: string): Promise<Task> {
    return this.request("GET", `/v1/tasks/${encodeURIComponent(taskID)}`);
  }

  appendEvent(event: UniversalEvent): Promise<{event: UniversalEvent; duplicate: boolean}> {
    return this.request("POST", `/v1/tasks/${encodeURIComponent(event.task_id)}/events`, event);
  }

  async listEvents(taskID: string, after = 0, includePayload = true): Promise<UniversalEvent[]> {
    const value = await this.request<{events: UniversalEvent[]}>("GET", `/v1/tasks/${encodeURIComponent(taskID)}/events?after=${after}&include_payload=${includePayload}`);
    return value.events;
  }

  continuityPackage(taskID: string, targetAgent = "generic"): Promise<ContinuityPackage> {
    return this.request("GET", `/v1/tasks/${encodeURIComponent(taskID)}/continuity-package?agent=${encodeURIComponent(targetAgent)}`);
  }

  async createInvitation(taskID: string, permission: "read" | "write", ttlSeconds = 900): Promise<string> {
    const value = await this.request<{token: string}>("POST", `/v1/tasks/${encodeURIComponent(taskID)}/invitations`, {permission, ttl_seconds: ttlSeconds});
    return value.token;
  }

  createQRInvitation(value: QRInvitation): Promise<{invitation: QRInvitation; qr_content: string; qr_svg: string}> {
    return this.request("POST", `/v1/tasks/${encodeURIComponent(value.task_id)}/qr-invitations`, value);
  }

  async listTaskPermissions(taskID: string): Promise<TaskPermission[]> {
    const value = await this.request<{permissions: TaskPermission[]}>("GET", `/v1/tasks/${encodeURIComponent(taskID)}/permissions`);
    return value.permissions;
  }

  putTaskPermission(taskID: string, accountID: string, permission: "read" | "write" | "admin"): Promise<TaskPermission> {
    return this.request("PUT", `/v1/tasks/${encodeURIComponent(taskID)}/permissions/${encodeURIComponent(accountID)}`, {permission});
  }

  deleteTaskPermission(taskID: string, accountID: string): Promise<void> {
    return this.request("DELETE", `/v1/tasks/${encodeURIComponent(taskID)}/permissions/${encodeURIComponent(accountID)}`);
  }

  exchangeSSO(idToken: string): Promise<{access_token: string; refresh_token: string; token_type: "Bearer"; expires_in: number; refresh_expires_in: number}> {
    return this.request("POST", "/v1/auth/sso/exchange", {id_token: idToken});
  }

  acceptInvitation(token: string): Promise<Record<string, unknown>> {
    return this.request("POST", "/v1/invitations/accept", {token});
  }

  async listOrganizations(): Promise<Record<string, unknown>[]> {
    const value = await this.request<{organizations: Record<string, unknown>[]}>("GET", "/v1/organizations");
    return value.organizations;
  }

  createOrganization(input: {name: string; slug: string; plan?: string; data_residency?: string; key_provider?: string; settings?: unknown}): Promise<Record<string, unknown>> {
    return this.request("POST", "/v1/organizations", input);
  }

  async listDevices(includeRevoked = false): Promise<Record<string, unknown>[]> {
    const value = await this.request<{devices: Record<string, unknown>[]}>('GET', `/v1/devices?include_revoked=${includeRevoked}`);
    return value.devices;
  }

  registerDevice(identity: DeviceIdentity): Promise<Record<string, unknown>> {
    return this.request("POST", "/v1/devices", {
      device_id: identity.device_id,
      name: identity.name,
      public_key: identity.signing_public_key,
      encryption_public_key: identity.encryption_public_key,
    });
  }

  revokeDevice(deviceID: string): Promise<void> {
    return this.request("DELETE", `/v1/devices/${encodeURIComponent(deviceID)}`);
  }

  async listAgents(): Promise<Record<string, unknown>[]> {
    const value = await this.request<{agents: Record<string, unknown>[]}>('GET', '/v1/agents');
    return value.agents;
  }

  createAgent(input: {name: string; provider: string; model?: string; capabilities: string[]; card?: unknown}): Promise<Record<string, unknown>> {
    return this.request("POST", "/v1/agents", input);
  }

  revokeAgent(agentID: string): Promise<void> {
    return this.request("DELETE", `/v1/agents/${encodeURIComponent(agentID)}`);
  }

  bindTaskAgent(taskID: string, input: {agent_id: string; role?: "contributor" | "reviewer"; native_session_ref?: string; resume_mode: "native" | "rehydrate" | "unsupported"; capabilities: string[]; metadata?: unknown}): Promise<Record<string, unknown>> {
    return this.request("POST", `/v1/tasks/${encodeURIComponent(taskID)}/agents`, input);
  }

  async listTaskAgents(taskID: string): Promise<Record<string, unknown>[]> {
    const value = await this.request<{agent_bindings: Record<string, unknown>[]}>('GET', `/v1/tasks/${encodeURIComponent(taskID)}/agents`);
    return value.agent_bindings;
  }

  syncHello(input: Record<string, unknown>): Promise<{protocol_version: string; capabilities: string[]; tasks: Array<{task: Task; server_cursor: number}>}> {
    return this.request("POST", "/v1/sync/hello", input);
  }

  syncPush(deviceID: string, events: UniversalEvent[]): Promise<{acknowledgements: Array<{event_id: string; accepted: boolean; duplicate: boolean; error_code?: string; error_message?: string; task_heads: string[]}>}> {
    return this.request("POST", "/v1/sync/push", {device_id: deviceID, events});
  }

  syncPull(deviceID: string, tasks: Array<{task_id: string; after: number; limit: number; include_payload: boolean}>): Promise<{batches: Array<{task: Task; events: UniversalEvent[]; next_cursor: number; has_more: boolean}>}> {
    return this.request("POST", "/v1/sync/pull", {device_id: deviceID, tasks});
  }

  syncAck(deviceID: string, tasks: Array<{task_id: string; cursor: number}>): Promise<void> {
    return this.request("POST", "/v1/sync/ack", {device_id: deviceID, tasks});
  }

  async listConflicts(taskID: string, includeResolved = false): Promise<Record<string, unknown>[]> {
    const value = await this.request<{conflicts: Record<string, unknown>[]}>('GET', `/v1/tasks/${encodeURIComponent(taskID)}/conflicts?include_resolved=${includeResolved}`);
    return value.conflicts;
  }

  async listBillingAccounts(): Promise<BillingAccount[]> {
    const value = await this.request<{billing_accounts: BillingAccount[]}>('GET', '/v1/billing/accounts');
    return value.billing_accounts;
  }

  createBillingAccount(input: {plan: string; currency?: string; organization_id?: string}): Promise<BillingAccount> {
    return this.request('POST', '/v1/billing/accounts', input);
  }

  createMarketplaceTransaction(input: {listing_id: string; idempotency_key: string; quantity?: number; buyer_agent_id?: string; seller_agent_id?: string; task_id?: string; metadata?: unknown}): Promise<MarketplaceTransaction> {
    return this.request('POST', '/v1/marketplace/transactions', input);
  }

  transitionMarketplaceTransaction(transactionID: string, action: 'authorize' | 'complete' | 'cancel'): Promise<MarketplaceTransaction> {
    return this.request('POST', `/v1/marketplace/transactions/${encodeURIComponent(transactionID)}/${action}`);
  }

  mcp(method: string, params: unknown, id: string | number = 1): Promise<unknown> {
    return this.request("POST", "/v1/mcp", {jsonrpc: "2.0", id, method, params});
  }
}

export interface KeyValueStore {
  get(key: string): Promise<string | null>;
  set(key: string, value: string): Promise<void>;
}

export class MemoryStore implements KeyValueStore {
  private readonly values = new Map<string, string>();
  async get(key: string): Promise<string | null> { return this.values.get(key) ?? null; }
  async set(key: string, value: string): Promise<void> { this.values.set(key, value); }
}

export class BrowserLocalStorage implements KeyValueStore {
  constructor(private readonly prefix = "agent-continuity:") {}
  async get(key: string): Promise<string | null> { return globalThis.localStorage.getItem(this.prefix + key); }
  async set(key: string, value: string): Promise<void> { globalThis.localStorage.setItem(this.prefix + key, value); }
}

type RuntimeState = {
  sequence: number;
  outbox: UniversalEvent[];
  inbox: UniversalEvent[];
  tasks: Record<string, {cursor: number; head_event_ids: string[]}>;
};

const EMPTY_STATE: RuntimeState = {sequence: 0, outbox: [], inbox: [], tasks: {}};
const encoder = new TextEncoder();

export async function generateDeviceIdentity(name: string, deviceID = `device_${crypto.randomUUID().replaceAll("-", "")}`): Promise<DeviceIdentity> {
  const signing = await crypto.subtle.generateKey({name: "Ed25519"}, true, ["sign", "verify"]) as CryptoKeyPair;
  const encryption = await crypto.subtle.generateKey({name: "X25519"}, true, ["deriveBits"]) as CryptoKeyPair;
  return {
    device_id: deviceID,
    name,
    signing_private_key: base64url(new Uint8Array(await crypto.subtle.exportKey("pkcs8", signing.privateKey))),
    signing_public_key: base64url(new Uint8Array(await crypto.subtle.exportKey("raw", signing.publicKey))),
    encryption_private_key: base64url(new Uint8Array(await crypto.subtle.exportKey("pkcs8", encryption.privateKey))),
    encryption_public_key: base64url(new Uint8Array(await crypto.subtle.exportKey("raw", encryption.publicKey))),
  };
}

export async function buildSignedEvent(input: {
  identity: DeviceIdentity;
  accountID: string;
  taskID: string;
  eventType: string;
  payload: unknown;
  sequence: number;
  parentEventIDs?: string[];
  branchID?: string;
  participantID?: string;
  agentID?: string;
  providerID?: string;
  sessionRef?: string;
  artifactRefs?: string[];
  extensions?: unknown;
  createdAt?: Date;
}): Promise<UniversalEvent> {
  const createdAt = input.createdAt ?? new Date();
  const payloadBytes = encoder.encode(wireJSON(input.payload));
  const eventID = `event_${crypto.randomUUID().replaceAll("-", "")}`;
  const event: UniversalEvent = {
    event_id: eventID,
    task_id: input.taskID,
    branch_id: input.branchID ?? "main",
    account_id: input.accountID,
    participant_id: input.participantID,
    agent_id: input.agentID,
    provider_id: input.providerID,
    device_id: input.identity.device_id,
    session_ref: input.sessionRef,
    parent_event_ids: input.parentEventIDs ?? [],
    logical_time: {wall_time_ms: createdAt.getTime(), counter: 0, node: input.identity.device_id},
    device_sequence: input.sequence,
    event_type: input.eventType,
    schema_version: "1.0",
    content_type: "application/json",
    content_digest: `sha256:${hex(new Uint8Array(await crypto.subtle.digest("SHA-256", payloadBytes)))}`,
    payload: input.payload,
    artifact_refs: input.artifactRefs ?? [],
    created_at_client: createdAt.toISOString(),
    signature: "",
    idempotency_key: eventID,
    extensions: input.extensions,
  };
  const privateKey = await crypto.subtle.importKey("pkcs8", asArrayBuffer(fromBase64url(input.identity.signing_private_key)), {name: "Ed25519"}, false, ["sign"]);
  event.signature = `ac1.${base64url(new Uint8Array(await crypto.subtle.sign("Ed25519", privateKey, asArrayBuffer(portableEventSigningBytes(event)))))}`;
  return event;
}

export function portableEventSigningBytes(event: UniversalEvent): Uint8Array {
  const fields = [
    "AC-EVENT-1", portableString(event.event_id), portableString(event.task_id), portableString(event.branch_id),
    portableString(event.account_id), portableString(String(event.participant_id ?? "")), portableString(String(event.agent_id ?? "")),
    portableString(String(event.provider_id ?? "")), portableString(event.device_id), portableString(String(event.session_ref ?? "")),
    portableStrings(event.parent_event_ids), String(event.logical_time.wall_time_ms), String(event.logical_time.counter), portableString(event.logical_time.node),
    String(event.device_sequence), portableString(event.event_type), portableString(event.schema_version), portableString(event.content_type), portableString(event.content_digest),
    base64url(encoder.encode(wireJSON(event.payload))), portableStrings((event.artifact_refs as string[] | undefined) ?? []),
    String(Date.parse(event.created_at_client)), portableString(event.idempotency_key), base64url(encoder.encode(wireJSON(event.extensions))),
  ];
  return encoder.encode(fields.join("\n"));
}

export async function signSyncHello(identity: DeviceIdentity, tasks: SyncTaskState[], clientVersion = `typescript/${SDK_VERSION}`, now = new Date()): Promise<Record<string, unknown>> {
  const value = {
    protocol_versions: ["1.0"], capabilities: ["batch_push", "cursor_pull", "missing_discovery", "artifact_manifests", "reconciliation", "e2ee"],
    client_version: clientVersion, device_id: identity.device_id, nonce: base64url(crypto.getRandomValues(new Uint8Array(24))), timestamp: now.toISOString(), tasks,
  };
  const privateKey = await crypto.subtle.importKey("pkcs8", asArrayBuffer(fromBase64url(identity.signing_private_key)), {name: "Ed25519"}, false, ["sign"]);
  const signature = await crypto.subtle.sign("Ed25519", privateKey, asArrayBuffer(portableSyncHelloSigningBytes(value)));
  return {...value, proof: `ac1.${base64url(new Uint8Array(signature))}`};
}

export function portableSyncHelloSigningBytes(value: {protocol_versions: string[]; capabilities: string[]; client_version: string; device_id: string; nonce: string; timestamp: string; tasks: SyncTaskState[]}): Uint8Array {
  const tasks = [...value.tasks].map(task => ({...task, head_event_ids: [...new Set(task.head_event_ids)].sort()})).sort((a, b) => a.task_id.localeCompare(b.task_id));
  const fields = ["AC-SYNC-HELLO-1", portableStrings([...new Set(value.protocol_versions)]), portableStrings([...new Set(value.capabilities)]), portableString(value.client_version), portableString(value.device_id), portableString(value.nonce), String(Date.parse(value.timestamp)), String(tasks.length)];
  for (const task of tasks) fields.push(portableString(task.task_id), portableStrings(task.head_event_ids), String(task.event_cursor));
  return encoder.encode(fields.join("\n"));
}

export class DeviceRuntime {
  constructor(
    public readonly client: Client,
    public readonly identity: DeviceIdentity,
    public readonly accountID: string,
    private readonly store: KeyValueStore,
    private readonly stateKey = `runtime:${identity.device_id}`,
  ) {}

  async register(): Promise<Record<string, unknown>> { return this.client.registerDevice(this.identity); }

  async queueEvent(taskID: string, eventType: string, payload: unknown, options: Omit<Parameters<typeof buildSignedEvent>[0], "identity" | "accountID" | "taskID" | "eventType" | "payload" | "sequence" | "parentEventIDs"> & {parentEventIDs?: string[]} = {}): Promise<UniversalEvent> {
    const state = await this.load();
    state.sequence += 1;
    const taskState = state.tasks[taskID] ?? {cursor: 0, head_event_ids: []};
    const event = await buildSignedEvent({...options, identity: this.identity, accountID: this.accountID, taskID, eventType, payload, sequence: state.sequence, parentEventIDs: options.parentEventIDs ?? taskState.head_event_ids});
    state.outbox.push(event);
    state.tasks[taskID] = {...taskState, head_event_ids: [event.event_id]};
    await this.save(state);
    return event;
  }

  async pendingEvents(): Promise<UniversalEvent[]> { return (await this.load()).outbox; }
  async receivedEvents(): Promise<UniversalEvent[]> { return (await this.load()).inbox; }

  async syncOnce(): Promise<SyncReport> {
    const state = await this.load();
    const report: SyncReport = {pushed: 0, duplicates: 0, rejected: 0, pulled: 0, acknowledged: 0};
    if (state.outbox.length) {
      const response = await this.client.syncPush(this.identity.device_id, state.outbox.slice(0, 100));
      const accepted = new Set(response.acknowledgements.filter(item => item.accepted).map(item => item.event_id));
      report.pushed = accepted.size;
      report.duplicates = response.acknowledgements.filter(item => item.accepted && item.duplicate).length;
      report.rejected = response.acknowledgements.filter(item => !item.accepted).length;
      state.outbox = state.outbox.filter(event => !accepted.has(event.event_id));
    }
    const taskStates = Object.entries(state.tasks).map(([task_id, value]) => ({task_id, head_event_ids: value.head_event_ids, event_cursor: value.cursor}));
    const hello = await this.client.syncHello(await signSyncHello(this.identity, taskStates));
    for (const plan of hello.tasks) {
      const local = state.tasks[plan.task.task_id] ?? {cursor: 0, head_event_ids: []};
      while (local.cursor < plan.server_cursor) {
        const pulled = await this.client.syncPull(this.identity.device_id, [{task_id: plan.task.task_id, after: local.cursor, limit: 200, include_payload: true}]);
        const batch = pulled.batches[0];
        if (!batch || batch.next_cursor <= local.cursor) break;
        state.inbox.push(...batch.events);
        local.cursor = batch.next_cursor;
        local.head_event_ids = batch.task.head_event_ids;
        report.pulled += batch.events.length;
        if (!batch.has_more) break;
      }
      state.tasks[plan.task.task_id] = local;
    }
    const acks = Object.entries(state.tasks).filter(([, value]) => value.cursor > 0).map(([task_id, value]) => ({task_id, cursor: value.cursor}));
    if (acks.length) { await this.client.syncAck(this.identity.device_id, acks); report.acknowledged = acks.length; }
    await this.save(state);
    return report;
  }

  private async load(): Promise<RuntimeState> {
    const raw = await this.store.get(this.stateKey);
    return raw ? JSON.parse(raw) as RuntimeState : structuredClone(EMPTY_STATE);
  }
  private async save(value: RuntimeState): Promise<void> { await this.store.set(this.stateKey, JSON.stringify(value)); }
}

function wireJSON(value: unknown): string { return value === undefined ? "" : JSON.stringify(value); }

function portableString(value: string): string { return base64url(encoder.encode(value)); }
function portableStrings(values: string[]): string { return [...values].sort().map(portableString).join("."); }
function hex(value: Uint8Array): string { return [...value].map(byte => byte.toString(16).padStart(2, "0")).join(""); }
function base64url(value: Uint8Array): string {
  let binary = "";
  for (const byte of value) binary += String.fromCharCode(byte);
  return btoa(binary).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/, "");
}
function fromBase64url(value: string): Uint8Array {
  const binary = atob(value.replaceAll("-", "+").replaceAll("_", "/") + "===".slice((value.length + 3) % 4));
  return Uint8Array.from(binary, character => character.charCodeAt(0));
}
function asArrayBuffer(value: Uint8Array): ArrayBuffer {
  return value.buffer.slice(value.byteOffset, value.byteOffset + value.byteLength) as ArrayBuffer;
}
