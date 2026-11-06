using System.Text.Json.Nodes;
using AgentContinuity;
using Org.BouncyCastle.Crypto.Parameters;
using Org.BouncyCastle.Crypto.Signers;

var identity = DeviceIdentity.Generate("Windows");
var value = Portable.BuildSignedEvent(identity, "acct_1", "task_1", "human.message.created", new JsonObject { ["text"] = "你好" }, 1, new EventOptions { Now = DateTimeOffset.FromUnixTimeMilliseconds(1_800_000_000_000) });
if (!value.Signature.StartsWith("ac1.")) throw new Exception("portable signature prefix missing");
var verifier = new Ed25519Signer(); verifier.Init(false, new Ed25519PublicKeyParameters(Portable.FromBase64Url(identity.SigningPublicKey), 0));
var bytes = Portable.EventSigningBytes(value); verifier.BlockUpdate(bytes, 0, bytes.Length);
if (!verifier.VerifySignature(Portable.FromBase64Url(value.Signature[4..]))) throw new Exception("signature verification failed");

var runtime = new DeviceRuntime(new AgentContinuityClient(new HttpClient(), "http://127.0.0.1:18080"), identity, "acct_1", new MemoryRuntimeStore());
await runtime.QueueEventAsync("task_1", "progress.updated", new JsonObject { ["percent"] = 20 });
if ((await runtime.PendingEventsAsync()).Count != 1) throw new Exception("offline queue did not persist");
Console.WriteLine("AgentContinuity .NET self-test passed");

var archiveClient = new AgentContinuityClient(new HttpClient(new ArchiveReceiptHandler()), "http://archive.test");
await archiveClient.WaitStorageJobAsync("task", 1, TimeSpan.FromSeconds(1));
bool rejected = false;
try { await archiveClient.WaitStorageJobAsync("task", 2, TimeSpan.FromSeconds(1)); }
catch (InvalidOperationException) { rejected = true; }
if (!rejected) throw new Exception("dead letter accepted as durable receipt");
Console.WriteLine("Archive receipt wait self-test passed");

var billingHandler = new BillingHandler();
var billingClient = new AgentContinuityClient(new HttpClient(billingHandler), "http://billing.test", "test-token");
await billingClient.RequestRefundAsync("inv/1", "pay1", 35, "unused", "stable-key");
await billingClient.RequestRefundAsync("inv/1", "pay1", 35, "unused", "stable-key");
if (billingHandler.Bodies[0] != billingHandler.Bodies[1]) throw new Exception("refund retry changed body");
if (await billingClient.InvoiceStatementAsync("inv/1") != "<html>receipt</html>") throw new Exception("statement not returned");
Console.WriteLine("Billing SDK self-test passed");

sealed class BillingHandler : HttpMessageHandler {
    public List<string> Bodies { get; } = [];
    protected override async Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken token) {
        if (request.Headers.Authorization?.ToString() != "Bearer test-token") throw new Exception("missing auth");
        var statement = request.RequestUri!.AbsolutePath.EndsWith("/statement");
        if (!statement) {
            if (request.Headers.GetValues("Idempotency-Key").Single() != "stable-key") throw new Exception("missing key");
            var body = await request.Content!.ReadAsStringAsync(token); Bodies.Add(body);
            if (JsonNode.Parse(body)!["amount_minor"]!.GetValue<long>() != 35) throw new Exception("wrong amount");
        }
        return new HttpResponseMessage(System.Net.HttpStatusCode.OK) { Content = new StringContent(statement ? "<html>receipt</html>" : "{\"refunds\":[]}") };
    }
}
sealed class ArchiveReceiptHandler : HttpMessageHandler {
    protected override Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken token) {
        var state = request.RequestUri!.AbsolutePath.EndsWith("/1") ? "stored" : "dead";
        return Task.FromResult(new HttpResponseMessage(System.Net.HttpStatusCode.OK) { Content = new StringContent($"{{\"id\":1,\"state\":\"{state}\"}}", System.Text.Encoding.UTF8, "application/json") });
    }
}
