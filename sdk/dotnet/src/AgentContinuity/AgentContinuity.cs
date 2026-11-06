using System.Net.Http.Json;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using System.Text.Json.Nodes;
using System.Text.Json.Serialization;
using Org.BouncyCastle.Crypto.Parameters;
using Org.BouncyCastle.Crypto.Signers;

namespace AgentContinuity;

public static class SDK
{
    public const string Version = "0.1.0";
    internal static readonly JsonSerializerOptions Json = new(JsonSerializerDefaults.Web)
    {
        DefaultIgnoreCondition = JsonIgnoreCondition.WhenWritingNull,
        PropertyNamingPolicy = JsonNamingPolicy.SnakeCaseLower,
    };
}

public sealed record DeviceIdentity(
    [property: JsonPropertyName("device_id")] string DeviceId,
    string Name,
    [property: JsonPropertyName("signing_private_key")] string SigningPrivateKey,
    [property: JsonPropertyName("signing_public_key")] string SigningPublicKey,
    [property: JsonPropertyName("encryption_private_key")] string EncryptionPrivateKey,
    [property: JsonPropertyName("encryption_public_key")] string EncryptionPublicKey)
{
    public static DeviceIdentity Generate(string name, string? deviceId = null)
    {
        var signing = new Ed25519PrivateKeyParameters(RandomNumberGenerator.GetBytes(Ed25519PrivateKeyParameters.KeySize), 0);
        var encryption = new X25519PrivateKeyParameters(RandomNumberGenerator.GetBytes(X25519PrivateKeyParameters.KeySize), 0);
        return new DeviceIdentity(
            deviceId ?? $"device_{Guid.NewGuid():N}", name,
            Portable.Base64Url(signing.GetEncoded()), Portable.Base64Url(signing.GeneratePublicKey().GetEncoded()),
            Portable.Base64Url(encryption.GetEncoded()), Portable.Base64Url(encryption.GeneratePublicKey().GetEncoded()));
    }
}

public sealed record LogicalTime(
    [property: JsonPropertyName("wall_time_ms")] long WallTimeMs,
    uint Counter,
    string Node);

public sealed record UniversalEvent
{
    [JsonPropertyName("event_id")] public required string EventId { get; init; }
    [JsonPropertyName("task_id")] public required string TaskId { get; init; }
    [JsonPropertyName("branch_id")] public required string BranchId { get; init; }
    [JsonPropertyName("account_id")] public required string AccountId { get; init; }
    [JsonPropertyName("participant_id")] public string? ParticipantId { get; init; }
    [JsonPropertyName("agent_id")] public string? AgentId { get; init; }
    [JsonPropertyName("provider_id")] public string? ProviderId { get; init; }
    [JsonPropertyName("device_id")] public required string DeviceId { get; init; }
    [JsonPropertyName("session_ref")] public string? SessionRef { get; init; }
    [JsonPropertyName("parent_event_ids")] public required List<string> ParentEventIds { get; init; }
    [JsonPropertyName("logical_time")] public required LogicalTime LogicalTime { get; init; }
    [JsonPropertyName("device_sequence")] public ulong DeviceSequence { get; init; }
    [JsonPropertyName("event_type")] public required string EventType { get; init; }
    [JsonPropertyName("schema_version")] public string SchemaVersion { get; init; } = "1.0";
    [JsonPropertyName("content_type")] public string ContentType { get; init; } = "application/json";
    [JsonPropertyName("content_digest")] public required string ContentDigest { get; init; }
    public JsonNode? Payload { get; init; }
    [JsonPropertyName("artifact_refs")] public List<string> ArtifactRefs { get; init; } = [];
    [JsonPropertyName("created_at_client")] public required string CreatedAtClient { get; init; }
    public required string Signature { get; set; }
    [JsonPropertyName("idempotency_key")] public required string IdempotencyKey { get; init; }
    public JsonNode? Extensions { get; init; }
}

public sealed record EventOptions
{
    public string BranchId { get; init; } = "main";
    public List<string> ParentEventIds { get; init; } = [];
    public string? ParticipantId { get; init; }
    public string? AgentId { get; init; }
    public string? ProviderId { get; init; }
    public string? SessionRef { get; init; }
    public List<string> ArtifactRefs { get; init; } = [];
    public JsonNode? Extensions { get; init; }
    public DateTimeOffset Now { get; init; } = DateTimeOffset.UtcNow;
}

public static class Portable
{
    public static UniversalEvent BuildSignedEvent(DeviceIdentity identity, string accountId, string taskId, string eventType, JsonNode payload, ulong sequence, EventOptions? options = null)
    {
        options ??= new EventOptions();
        var eventId = $"event_{Guid.NewGuid():N}";
        var payloadBytes = WireJson(payload);
        var value = new UniversalEvent
        {
            EventId = eventId, TaskId = taskId, BranchId = options.BranchId, AccountId = accountId,
            ParticipantId = options.ParticipantId, AgentId = options.AgentId, ProviderId = options.ProviderId,
            DeviceId = identity.DeviceId, SessionRef = options.SessionRef, ParentEventIds = options.ParentEventIds,
            LogicalTime = new LogicalTime(options.Now.ToUnixTimeMilliseconds(), 0, identity.DeviceId), DeviceSequence = sequence,
            EventType = eventType, ContentDigest = $"sha256:{Convert.ToHexString(SHA256.HashData(payloadBytes)).ToLowerInvariant()}",
            Payload = payload, ArtifactRefs = options.ArtifactRefs, CreatedAtClient = options.Now.UtcDateTime.ToString("yyyy-MM-dd'T'HH:mm:ss.fff'Z'"),
            Signature = "", IdempotencyKey = eventId, Extensions = options.Extensions,
        };
        var signer = new Ed25519Signer();
        signer.Init(true, new Ed25519PrivateKeyParameters(FromBase64Url(identity.SigningPrivateKey), 0));
        var bytes = EventSigningBytes(value); signer.BlockUpdate(bytes, 0, bytes.Length);
        value.Signature = $"ac1.{Base64Url(signer.GenerateSignature())}";
        return value;
    }

    public static byte[] EventSigningBytes(UniversalEvent value)
    {
        var fields = new[]
        {
            "AC-EVENT-1", String(value.EventId), String(value.TaskId), String(value.BranchId), String(value.AccountId),
            String(value.ParticipantId ?? ""), String(value.AgentId ?? ""), String(value.ProviderId ?? ""), String(value.DeviceId), String(value.SessionRef ?? ""),
            Strings(value.ParentEventIds), value.LogicalTime.WallTimeMs.ToString(), value.LogicalTime.Counter.ToString(), String(value.LogicalTime.Node), value.DeviceSequence.ToString(),
            String(value.EventType), String(value.SchemaVersion), String(value.ContentType), String(value.ContentDigest),
            Base64Url(WireJson(value.Payload)), Strings(value.ArtifactRefs), DateTimeOffset.Parse(value.CreatedAtClient).ToUnixTimeMilliseconds().ToString(), String(value.IdempotencyKey),
            Base64Url(WireJson(value.Extensions)),
        };
        return Encoding.UTF8.GetBytes(string.Join('\n', fields));
    }

    public static SyncHelloRequest SignHello(DeviceIdentity identity, IEnumerable<SyncTaskState> tasks, string clientVersion = "dotnet/0.1.0", DateTimeOffset? now = null)
    {
        var value = new SyncHelloRequest(["1.0"], SyncCapabilities.All, clientVersion, identity.DeviceId, Base64Url(RandomNumberGenerator.GetBytes(24)), (now ?? DateTimeOffset.UtcNow).UtcDateTime.ToString("yyyy-MM-dd'T'HH:mm:ss.fff'Z'"), tasks.ToList(), "");
        var signer = new Ed25519Signer(); signer.Init(true, new Ed25519PrivateKeyParameters(FromBase64Url(identity.SigningPrivateKey), 0));
        var bytes = HelloSigningBytes(value); signer.BlockUpdate(bytes, 0, bytes.Length);
        return value with { Proof = $"ac1.{Base64Url(signer.GenerateSignature())}" };
    }

    public static byte[] HelloSigningBytes(SyncHelloRequest value)
    {
        var tasks = value.Tasks.Select(task => task with { HeadEventIds = task.HeadEventIds.Distinct().Order().ToList() }).OrderBy(task => task.TaskId).ToList();
        var fields = new List<string> { "AC-SYNC-HELLO-1", Strings(value.ProtocolVersions.Distinct()), Strings(value.Capabilities.Distinct()), String(value.ClientVersion), String(value.DeviceId), String(value.Nonce), DateTimeOffset.Parse(value.Timestamp).ToUnixTimeMilliseconds().ToString(), tasks.Count.ToString() };
        foreach (var task in tasks) fields.AddRange([String(task.TaskId), Strings(task.HeadEventIds), task.EventCursor.ToString()]);
        return Encoding.UTF8.GetBytes(string.Join('\n', fields));
    }

    internal static byte[] WireJson(JsonNode? value) => value is null ? [] : Encoding.UTF8.GetBytes(value.ToJsonString(SDK.Json));
    internal static string String(string value) => Base64Url(Encoding.UTF8.GetBytes(value));
    internal static string Strings(IEnumerable<string> values) => string.Join('.', values.Order().Select(String));
    public static string Base64Url(byte[] value) => Convert.ToBase64String(value).TrimEnd('=').Replace('+', '-').Replace('/', '_');
    public static byte[] FromBase64Url(string value) => Convert.FromBase64String(value.Replace('-', '+').Replace('_', '/') + new string('=', (4 - value.Length % 4) % 4));
}

public static class SyncCapabilities
{
    public static readonly List<string> All = ["batch_push", "cursor_pull", "missing_discovery", "artifact_manifests", "reconciliation", "e2ee"];
}

public sealed record SyncTaskState(
    [property: JsonPropertyName("task_id")] string TaskId,
    [property: JsonPropertyName("head_event_ids")] List<string> HeadEventIds,
    [property: JsonPropertyName("event_cursor")] ulong EventCursor);
public sealed record SyncHelloRequest(
    [property: JsonPropertyName("protocol_versions")] List<string> ProtocolVersions,
    List<string> Capabilities,
    [property: JsonPropertyName("client_version")] string ClientVersion,
    [property: JsonPropertyName("device_id")] string DeviceId,
    string Nonce,
    string Timestamp,
    List<SyncTaskState> Tasks,
    string Proof);
public sealed record TaskSummary([property: JsonPropertyName("task_id")] string TaskId, [property: JsonPropertyName("head_event_ids")] List<string> HeadEventIds);
public sealed record SyncTaskPlan(TaskSummary Task, [property: JsonPropertyName("server_cursor")] ulong ServerCursor);
public sealed record SyncHelloResponse([property: JsonPropertyName("protocol_version")] string ProtocolVersion, List<string> Capabilities, List<SyncTaskPlan> Tasks);
public sealed record SyncAcknowledgement([property: JsonPropertyName("event_id")] string EventId, bool Accepted, bool Duplicate);
public sealed record SyncPushResponse(List<SyncAcknowledgement> Acknowledgements);
public sealed record PullCursor([property: JsonPropertyName("task_id")] string TaskId, ulong After, int Limit = 200, [property: JsonPropertyName("include_payload")] bool IncludePayload = true);
public sealed record SyncBatch(TaskSummary Task, List<UniversalEvent> Events, [property: JsonPropertyName("next_cursor")] ulong NextCursor, [property: JsonPropertyName("has_more")] bool HasMore);
public sealed record SyncPullResponse(List<SyncBatch> Batches);
public sealed record AckCursor([property: JsonPropertyName("task_id")] string TaskId, ulong Cursor);

public sealed class ApiException(int status, string code, string message) : Exception($"API {status} {code}: {message}")
{
    public int Status { get; } = status;
    public string Code { get; } = code;
}

public sealed class AgentContinuityClient(HttpClient httpClient, string baseUrl, string token = "")
{
    public Task<JsonObject> ListStorageJobsAsync(string taskId, long after = 0, CancellationToken cancellation = default) => RequestAsync<JsonObject>(HttpMethod.Get, $"/v1/tasks/{Uri.EscapeDataString(taskId)}/storage-jobs?after={after}", null, cancellation);
    public Task<JsonObject> GetStorageJobAsync(string taskId, long jobId, CancellationToken cancellation = default) => RequestAsync<JsonObject>(HttpMethod.Get, $"/v1/tasks/{Uri.EscapeDataString(taskId)}/storage-jobs/{jobId}", null, cancellation);
    public async Task<JsonObject> WaitStorageJobAsync(string taskId, long jobId, TimeSpan timeout, CancellationToken cancellation = default)
    {
        if (timeout <= TimeSpan.Zero) throw new ArgumentOutOfRangeException(nameof(timeout));
        using var linked = CancellationTokenSource.CreateLinkedTokenSource(cancellation);
        linked.CancelAfter(timeout);
        while (true) {
            var job = await GetStorageJobAsync(taskId, jobId, linked.Token);
            var state = job["state"]?.GetValue<string>();
            if (state == "stored") return job;
            if (state is "dead" or "deleted") throw new InvalidOperationException($"Archive job ended in {state}");
            await Task.Delay(1000, linked.Token);
        }
    }
    public Task<JsonObject> RetryStorageJobAsync(string taskId, long jobId, CancellationToken cancellation = default) => RequestAsync<JsonObject>(HttpMethod.Post, $"/v1/tasks/{Uri.EscapeDataString(taskId)}/storage-jobs/{jobId}/retry", null, cancellation);
    public Task<JsonObject> UploadContentAsync(string taskId, string artifactId, string versionId, string filename, string contentType, byte[] content, string parentVersionId = "", CancellationToken cancellation = default)
    {
        if (content.Length > 8 * 1024 * 1024) throw new ArgumentException("Use resumable upload for content larger than 8 MiB");
        return RequestAsync<JsonObject>(HttpMethod.Post, $"/v1/tasks/{Uri.EscapeDataString(taskId)}/contents", new { artifact_id = artifactId, version_id = versionId, filename, content_type = contentType, content_base64 = Convert.ToBase64String(content), parent_version_id = parentVersionId }, cancellation);
    }
    public HttpClient Http { get; } = httpClient;
    public string BaseUrl { get; } = baseUrl.TrimEnd('/');
    public string Token { get; set; } = token;

    public Task<JsonObject> ExchangeSsoAsync(string idToken, CancellationToken cancellation = default) => RequestAsync<JsonObject>(HttpMethod.Post, "/v1/auth/sso/exchange", new { id_token = idToken }, cancellation);
    public Task<JsonObject> RefreshTokenAsync(string refreshToken, CancellationToken cancellation = default) => RequestAsync<JsonObject>(HttpMethod.Post, "/v1/auth/refresh", new { refresh_token = refreshToken }, cancellation);
    public Task LogoutAsync(string refreshToken, CancellationToken cancellation = default) => RequestAsync(HttpMethod.Post, "/v1/auth/logout", new { refresh_token = refreshToken }, cancellation);
    public Task<JsonObject> RegisterDeviceAsync(DeviceIdentity value, CancellationToken cancellation = default) => RequestAsync<JsonObject>(HttpMethod.Post, "/v1/devices", new { device_id = value.DeviceId, name = value.Name, public_key = value.SigningPublicKey, encryption_public_key = value.EncryptionPublicKey }, cancellation);
    public Task<JsonObject> CreateTaskAsync(string title, string objective, string originDevice, CancellationToken cancellation = default) => RequestAsync<JsonObject>(HttpMethod.Post, "/v1/tasks", new { title, objective, origin_device = originDevice }, cancellation);
    public Task<JsonObject> AppendEventAsync(UniversalEvent value, CancellationToken cancellation = default) => RequestAsync<JsonObject>(HttpMethod.Post, $"/v1/tasks/{Uri.EscapeDataString(value.TaskId)}/events", value, cancellation);
    public Task<JsonObject> CreateAgentAsync(string name, string provider, List<string> capabilities, string model = "", CancellationToken cancellation = default) => RequestAsync<JsonObject>(HttpMethod.Post, "/v1/agents", new { name, provider, model, capabilities }, cancellation);
    public Task<JsonObject> BindTaskAgentAsync(string taskId, string agentId, string resumeMode, List<string> capabilities, string nativeSessionRef = "", CancellationToken cancellation = default) => RequestAsync<JsonObject>(HttpMethod.Post, $"/v1/tasks/{Uri.EscapeDataString(taskId)}/agents", new { agent_id = agentId, role = "contributor", native_session_ref = nativeSessionRef, resume_mode = resumeMode, capabilities }, cancellation);
    public Task<JsonObject> ContinuityPackageAsync(string taskId, string targetAgent = "generic", CancellationToken cancellation = default) => RequestAsync<JsonObject>(HttpMethod.Get, $"/v1/tasks/{Uri.EscapeDataString(taskId)}/continuity-package?agent={Uri.EscapeDataString(targetAgent)}", null, cancellation);
    public Task<SyncHelloResponse> SyncHelloAsync(SyncHelloRequest value, CancellationToken cancellation = default) => RequestAsync<SyncHelloResponse>(HttpMethod.Post, "/v1/sync/hello", value, cancellation);
    public Task<SyncPushResponse> SyncPushAsync(string deviceId, List<UniversalEvent> events, CancellationToken cancellation = default) => RequestAsync<SyncPushResponse>(HttpMethod.Post, "/v1/sync/push", new { device_id = deviceId, events }, cancellation);
    public Task<SyncPullResponse> SyncPullAsync(string deviceId, List<PullCursor> tasks, CancellationToken cancellation = default) => RequestAsync<SyncPullResponse>(HttpMethod.Post, "/v1/sync/pull", new { device_id = deviceId, tasks }, cancellation);
    public async Task SyncAckAsync(string deviceId, List<AckCursor> tasks, CancellationToken cancellation = default) => await RequestAsync(HttpMethod.Post, "/v1/sync/ack", new { device_id = deviceId, tasks }, cancellation);

    public Task<JsonObject> CreateRunAsync(JsonObject plan, string key, CancellationToken cancellation = default) => RequestAsync<JsonObject>(HttpMethod.Post, "/v1/orchestration/runs", plan, cancellation, key);
    public Task<JsonObject> GetRunAsync(string id, CancellationToken cancellation = default) => RequestAsync<JsonObject>(HttpMethod.Get, $"/v1/orchestration/runs/{Uri.EscapeDataString(id)}", null, cancellation);
    public Task<JsonObject> ControlRunAsync(string id, string action, CancellationToken cancellation = default) => RequestAsync<JsonObject>(HttpMethod.Post, $"/v1/orchestration/runs/{Uri.EscapeDataString(id)}/{Uri.EscapeDataString(action)}", null, cancellation);
    public Task<JsonObject> IssueFiscalAsync(string id, JsonObject buyer, CancellationToken cancellation = default) => RequestAsync<JsonObject>(HttpMethod.Post, $"/v1/billing/invoices/{Uri.EscapeDataString(id)}/fiscal", buyer, cancellation);
    public Task<JsonObject> GetSettlementAsync(string id, CancellationToken cancellation = default) => RequestAsync<JsonObject>(HttpMethod.Get, $"/v1/commerce/settlements/{Uri.EscapeDataString(id)}", null, cancellation);
    public Task<JsonObject> ListInvoicesAsync(CancellationToken cancellation = default) => RequestAsync<JsonObject>(HttpMethod.Get, "/v1/billing/invoices?limit=200", null, cancellation);
    public Task<JsonObject> GetInvoiceAsync(string id, CancellationToken cancellation = default) => RequestAsync<JsonObject>(HttpMethod.Get, $"/v1/billing/invoices/{Uri.EscapeDataString(id)}", null, cancellation);
    public Task<JsonObject> QuoteInvoiceAsync(JsonObject period, CancellationToken cancellation = default) => RequestAsync<JsonObject>(HttpMethod.Post, "/v1/billing/quote", period, cancellation);
    public Task<JsonObject> IssueInvoiceAsync(JsonObject period, string key, CancellationToken cancellation = default) => RequestAsync<JsonObject>(HttpMethod.Post, "/v1/billing/invoices", period, cancellation, key);
    public Task<JsonObject> CreatePaymentAsync(string id, string provider, string key, CancellationToken cancellation = default) => RequestAsync<JsonObject>(HttpMethod.Post, $"/v1/billing/invoices/{Uri.EscapeDataString(id)}/payments", new { provider }, cancellation, key);
    public Task<JsonObject> ReconcilePaymentAsync(string id, string payment, CancellationToken cancellation = default) => RequestAsync<JsonObject>(HttpMethod.Post, $"/v1/billing/invoices/{Uri.EscapeDataString(id)}/payments/{Uri.EscapeDataString(payment)}/reconcile", null, cancellation);
    public Task<JsonObject> RequestRefundAsync(string id, string payment, long amountMinor, string reason, string key, CancellationToken cancellation = default) => RequestAsync<JsonObject>(HttpMethod.Post, $"/v1/billing/invoices/{Uri.EscapeDataString(id)}/refunds", new { payment_id = payment, amount_minor = amountMinor, reason }, cancellation, key);
    public Task<JsonObject> ReconcileRefundAsync(string id, string refund, CancellationToken cancellation = default) => RequestAsync<JsonObject>(HttpMethod.Post, $"/v1/billing/invoices/{Uri.EscapeDataString(id)}/refunds/{Uri.EscapeDataString(refund)}/reconcile", null, cancellation);
    public async Task<string> InvoiceStatementAsync(string id, CancellationToken cancellation = default) {
        using var response = await SendAsync(HttpMethod.Get, $"/v1/billing/invoices/{Uri.EscapeDataString(id)}/statement", null, cancellation);
        return await response.Content.ReadAsStringAsync(cancellation);
    }

    private async Task<T> RequestAsync<T>(HttpMethod method, string path, object? body, CancellationToken cancellation, string? key = null)
    {
        using var response = await SendAsync(method, path, body, cancellation, key); var data = await response.Content.ReadAsByteArrayAsync(cancellation);
        if (data.Length == 0) throw new InvalidDataException("API returned an empty response");
        return JsonSerializer.Deserialize<T>(data, SDK.Json) ?? throw new InvalidDataException("API returned invalid JSON");
    }
    private async Task RequestAsync(HttpMethod method, string path, object? body, CancellationToken cancellation) { using var response = await SendAsync(method, path, body, cancellation); }
    private async Task<HttpResponseMessage> SendAsync(HttpMethod method, string path, object? body, CancellationToken cancellation, string? key = null)
    {
        using var request = new HttpRequestMessage(method, BaseUrl + path); request.Headers.Accept.ParseAdd("application/json"); request.Headers.UserAgent.ParseAdd($"agent-continuity-dotnet/{SDK.Version}");
        if (key is not null) request.Headers.Add("Idempotency-Key", key);
        if (!string.IsNullOrEmpty(Token)) request.Headers.Authorization = new("Bearer", Token);
        if (body is not null) request.Content = JsonContent.Create(body, options: SDK.Json);
        var response = await Http.SendAsync(request, cancellation);
        if (!response.IsSuccessStatusCode) { var text = await response.Content.ReadAsStringAsync(cancellation); JsonNode? value = null; try { value = JsonNode.Parse(text); } catch (JsonException) { } throw new ApiException((int)response.StatusCode, value?["error"]?["code"]?.GetValue<string>() ?? "http_error", value?["error"]?["message"]?.GetValue<string>() ?? response.ReasonPhrase ?? "HTTP error"); }
        return response;
    }
}

public interface IRuntimeStore { Task<string?> LoadAsync(string key); Task SaveAsync(string key, string value); }
public sealed class MemoryRuntimeStore : IRuntimeStore
{
    private readonly Dictionary<string, string> values = [];
    public Task<string?> LoadAsync(string key) { lock (values) return Task.FromResult(values.GetValueOrDefault(key)); }
    public Task SaveAsync(string key, string value) { lock (values) values[key] = value; return Task.CompletedTask; }
}
public sealed class FileRuntimeStore(string directory) : IRuntimeStore
{
    private readonly string root = Directory.CreateDirectory(directory).FullName;
    private string PathFor(string key) => Path.Combine(root, Convert.ToHexString(SHA256.HashData(Encoding.UTF8.GetBytes(key))).ToLowerInvariant() + ".json");
    public async Task<string?> LoadAsync(string key) => File.Exists(PathFor(key)) ? await File.ReadAllTextAsync(PathFor(key)) : null;
    public async Task SaveAsync(string key, string value) { var temporary = Path.Combine(root, $".state-{Guid.NewGuid():N}"); await File.WriteAllTextAsync(temporary, value); File.Move(temporary, PathFor(key), true); }
}

public sealed record SyncReport(int Pushed = 0, int Duplicates = 0, int Rejected = 0, int Pulled = 0, int Acknowledged = 0);
internal sealed record LocalTaskState(ulong Cursor = 0, List<string>? HeadEventIds = null);
internal sealed record RuntimeState(ulong Sequence = 0, List<UniversalEvent>? Outbox = null, List<UniversalEvent>? Inbox = null, Dictionary<string, LocalTaskState>? Tasks = null);

public sealed class DeviceRuntime(AgentContinuityClient client, DeviceIdentity identity, string accountId, IRuntimeStore store)
{
    public AgentContinuityClient Client { get; } = client;
    public DeviceIdentity Identity { get; } = identity;
    public string AccountId { get; } = accountId;
    private readonly SemaphoreSlim mutex = new(1, 1);
    private string Key => $"runtime:{Identity.DeviceId}";

    public Task<JsonObject> RegisterAsync(CancellationToken cancellation = default) => Client.RegisterDeviceAsync(Identity, cancellation);
    public async Task<UniversalEvent> QueueEventAsync(string taskId, string eventType, JsonNode payload, EventOptions? options = null)
    {
        await mutex.WaitAsync(); try {
            var state = await LoadAsync(); state = state with { Sequence = state.Sequence + 1 };
            var tasks = state.Tasks!; var outbox = state.Outbox!;
            var task = tasks.GetValueOrDefault(taskId) ?? new LocalTaskState(0, []);
            options ??= new EventOptions(); if (options.ParentEventIds.Count == 0) options = options with { ParentEventIds = task.HeadEventIds ?? [] };
            var value = Portable.BuildSignedEvent(Identity, AccountId, taskId, eventType, payload, state.Sequence, options);
            outbox.Add(value); tasks[taskId] = task with { HeadEventIds = [value.EventId] }; await SaveAsync(state); return value;
        } finally { mutex.Release(); }
    }
    public async Task<IReadOnlyList<UniversalEvent>> PendingEventsAsync() => (await LoadAsync()).Outbox!;
    public async Task<IReadOnlyList<UniversalEvent>> ReceivedEventsAsync() => (await LoadAsync()).Inbox!;
    public async Task<SyncReport> SyncOnceAsync(CancellationToken cancellation = default)
    {
        await mutex.WaitAsync(cancellation); try {
            var state = await LoadAsync(); var report = new SyncReport(); var outbox = state.Outbox!; var inbox = state.Inbox!; var tasks = state.Tasks!;
            if (outbox.Count > 0) {
                var response = await Client.SyncPushAsync(Identity.DeviceId, outbox.Take(100).ToList(), cancellation); var accepted = response.Acknowledgements.Where(item => item.Accepted).Select(item => item.EventId).ToHashSet();
                report = report with { Pushed = accepted.Count, Duplicates = response.Acknowledgements.Count(item => item.Accepted && item.Duplicate), Rejected = response.Acknowledgements.Count(item => !item.Accepted) }; outbox.RemoveAll(item => accepted.Contains(item.EventId));
            }
            var taskStates = tasks.Select(item => new SyncTaskState(item.Key, item.Value.HeadEventIds ?? [], item.Value.Cursor)).ToList();
            var hello = await Client.SyncHelloAsync(Portable.SignHello(Identity, taskStates), cancellation);
            foreach (var plan in hello.Tasks) {
                var local = tasks.GetValueOrDefault(plan.Task.TaskId) ?? new LocalTaskState(0, []);
                while (local.Cursor < plan.ServerCursor) {
                    var response = await Client.SyncPullAsync(Identity.DeviceId, [new PullCursor(plan.Task.TaskId, local.Cursor)], cancellation); var batch = response.Batches.FirstOrDefault(); if (batch is null || batch.NextCursor <= local.Cursor) break;
                    inbox.AddRange(batch.Events); local = new LocalTaskState(batch.NextCursor, batch.Task.HeadEventIds); report = report with { Pulled = report.Pulled + batch.Events.Count }; if (!batch.HasMore) break;
                }
                tasks[plan.Task.TaskId] = local;
            }
            var acks = tasks.Where(item => item.Value.Cursor > 0).Select(item => new AckCursor(item.Key, item.Value.Cursor)).ToList();
            if (acks.Count > 0) { await Client.SyncAckAsync(Identity.DeviceId, acks, cancellation); report = report with { Acknowledged = acks.Count }; }
            await SaveAsync(state); return report;
        } finally { mutex.Release(); }
    }
    private async Task<RuntimeState> LoadAsync() { var raw = await store.LoadAsync(Key); var value = raw is null ? new RuntimeState() : JsonSerializer.Deserialize<RuntimeState>(raw, SDK.Json)!; return value with { Outbox = value.Outbox ?? [], Inbox = value.Inbox ?? [], Tasks = value.Tasks ?? [] }; }
    private Task SaveAsync(RuntimeState value) => store.SaveAsync(Key, JsonSerializer.Serialize(value, SDK.Json));
}
