import AgentContinuity
import CryptoKit
import Foundation

@main struct AgentContinuitySelfTest {
    static func main() async throws {
        let identity = try DeviceIdentity.generate(name: "iPhone")
        var options = EventOptions()
        options.now = Date(timeIntervalSince1970: 1_800_000_000)
        let event = try PortableProtocol.buildSignedEvent(identity: identity, accountID: "acct_1", taskID: "task_1", eventType: "human.message.created", payload: .object(["text": .string("你好")]), sequence: 1, options: options)
        precondition(event.signature.hasPrefix("ac1."))
        let signature = try Data(base64URL: String(event.signature.dropFirst(4)))
        let publicKey = try Curve25519.Signing.PublicKey(rawRepresentation: Data(base64URL: identity.signingPublicKey))
        let signingBytes = try PortableProtocol.eventSigningBytes(event)
        precondition(publicKey.isValidSignature(signature, for: signingBytes))

        let client = AgentContinuityClient(baseURL: URL(string: "http://127.0.0.1:18080")!)
        let runtime = DeviceRuntime(client: client, identity: identity, accountID: "acct_1", store: MemoryRuntimeStore())
        _ = try await runtime.queueEvent(taskID: "task_1", eventType: "progress.updated", payload: .object(["percent": .number(20)]))
        let pending = try await runtime.pendingEvents()
        precondition(pending.count == 1)
        print("AgentContinuity Swift self-test passed")
    }
}
