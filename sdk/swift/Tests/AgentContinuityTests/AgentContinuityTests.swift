import AgentContinuity
import CryptoKit
import Foundation

final class ArchiveReceiptProtocol: URLProtocol {
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        if request.url!.path.contains("/billing/") {
            precondition(request.value(forHTTPHeaderField: "Authorization") == "Bearer test-token")
            let statement = request.url!.lastPathComponent == "statement"
            if !statement { precondition(request.value(forHTTPHeaderField: "Idempotency-Key") == "stable-key") }
            client?.urlProtocol(self, didReceive: HTTPURLResponse(url: request.url!,statusCode:200,httpVersion:nil,headerFields:nil)!,cacheStoragePolicy:.notAllowed)
            client?.urlProtocol(self, didLoad: Data((statement ? "<html>receipt</html>" : "{\"refunds\":[]}").utf8))
            client?.urlProtocolDidFinishLoading(self); return
        }
        let state = request.url!.lastPathComponent == "1" ? "stored" : "dead"
        client?.urlProtocol(self, didReceive: HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil, headerFields: ["Content-Type":"application/json"])!, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data("{\"id\":1,\"state\":\"\(state)\"}".utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

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
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [ArchiveReceiptProtocol.self]
        let archiveClient = AgentContinuityClient(baseURL: URL(string: "http://archive.test")!, session: URLSession(configuration: config))
        _ = try await archiveClient.waitStorageJob(taskID: "task", jobID: 1, timeout: 1)
        do { _ = try await archiveClient.waitStorageJob(taskID: "task", jobID: 2, timeout: 1); preconditionFailure("dead letter accepted") }
        catch SDKError.http(let status, _) { precondition(status == 409) }
        let billingClient = AgentContinuityClient(baseURL: URL(string:"http://billing.test")!, token:"test-token", session: URLSession(configuration:config))
        for _ in 0..<2 { _ = try await billingClient.requestRefund(invoiceID:"inv1",paymentID:"pay1",amountMinor:35,reason:"unused",key:"stable-key") }
        let statement = try await billingClient.invoiceStatement("inv1")
        precondition(statement == "<html>receipt</html>")
        print("AgentContinuity Swift self-test passed (including billing)")
    }
}
