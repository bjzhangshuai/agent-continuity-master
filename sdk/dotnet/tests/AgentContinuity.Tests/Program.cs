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
