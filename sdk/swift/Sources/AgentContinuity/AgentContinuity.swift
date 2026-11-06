import CryptoKit
import Foundation
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif
#if canImport(Security)
import Security
#endif

public let agentContinuitySDKVersion = "0.1.0"

public enum JSONValue: Codable, Equatable, Sendable {
    case string(String), number(Double), bool(Bool), object([String: JSONValue]), array([JSONValue]), null

    public init(from decoder: Decoder) throws {
        let container = try decoder.singleValueContainer()
        if container.decodeNil() { self = .null }
        else if let value = try? container.decode(Bool.self) { self = .bool(value) }
        else if let value = try? container.decode(Double.self) { self = .number(value) }
        else if let value = try? container.decode(String.self) { self = .string(value) }
        else if let value = try? container.decode([String: JSONValue].self) { self = .object(value) }
        else { self = .array(try container.decode([JSONValue].self)) }
    }

    public func encode(to encoder: Encoder) throws {
        var container = encoder.singleValueContainer()
        switch self {
        case .string(let value): try container.encode(value)
        case .number(let value): try container.encode(value)
        case .bool(let value): try container.encode(value)
        case .object(let value): try container.encode(value)
        case .array(let value): try container.encode(value)
        case .null: try container.encodeNil()
        }
    }
}

public struct DeviceIdentity: Codable, Equatable, Sendable {
    public let deviceID: String
    public let name: String
    public let signingPrivateKey: String
    public let signingPublicKey: String
    public let encryptionPrivateKey: String
    public let encryptionPublicKey: String

    enum CodingKeys: String, CodingKey {
        case deviceID = "device_id", name
        case signingPrivateKey = "signing_private_key", signingPublicKey = "signing_public_key"
        case encryptionPrivateKey = "encryption_private_key", encryptionPublicKey = "encryption_public_key"
    }

    public static func generate(name: String, deviceID: String = "") throws -> DeviceIdentity {
        let signing = Curve25519.Signing.PrivateKey()
        let encryption = Curve25519.KeyAgreement.PrivateKey()
        return DeviceIdentity(
            deviceID: deviceID.isEmpty ? "device_\(UUID().uuidString.replacingOccurrences(of: "-", with: "").lowercased())" : deviceID,
            name: name,
            signingPrivateKey: signing.rawRepresentation.base64URL,
            signingPublicKey: signing.publicKey.rawRepresentation.base64URL,
            encryptionPrivateKey: encryption.rawRepresentation.base64URL,
            encryptionPublicKey: encryption.publicKey.rawRepresentation.base64URL
        )
    }
}

public struct LogicalTime: Codable, Equatable, Sendable {
    public var wallTimeMS: Int64
    public var counter: UInt32
    public var node: String
    enum CodingKeys: String, CodingKey { case wallTimeMS = "wall_time_ms", counter, node }
}

public struct UniversalEvent: Codable, Equatable, Sendable {
    public var eventID: String
    public var taskID: String
    public var branchID: String
    public var accountID: String
    public var participantID: String?
    public var agentID: String?
    public var providerID: String?
    public var deviceID: String
    public var sessionRef: String?
    public var parentEventIDs: [String]
    public var logicalTime: LogicalTime
    public var deviceSequence: UInt64
    public var eventType: String
    public var schemaVersion: String
    public var contentType: String
    public var contentDigest: String
    public var payload: JSONValue?
    public var artifactRefs: [String]
    public var createdAtClient: String
    public var signature: String
    public var idempotencyKey: String
    public var extensions: JSONValue?

    enum CodingKeys: String, CodingKey {
        case eventID = "event_id", taskID = "task_id", branchID = "branch_id", accountID = "account_id"
        case participantID = "participant_id", agentID = "agent_id", providerID = "provider_id", deviceID = "device_id"
        case sessionRef = "session_ref", parentEventIDs = "parent_event_ids", logicalTime = "logical_time", deviceSequence = "device_sequence"
        case eventType = "event_type", schemaVersion = "schema_version", contentType = "content_type", contentDigest = "content_digest"
        case payload, artifactRefs = "artifact_refs", createdAtClient = "created_at_client", signature, idempotencyKey = "idempotency_key", extensions
    }
}

public struct EventOptions: Sendable {
    public var branchID = "main"
    public var parentEventIDs: [String] = []
    public var participantID: String?
    public var agentID: String?
    public var providerID: String?
    public var sessionRef: String?
    public var artifactRefs: [String] = []
    public var extensions: JSONValue?
    public var now = Date()
    public init() {}
}

public enum PortableProtocol {
    public static func buildSignedEvent(identity: DeviceIdentity, accountID: String, taskID: String, eventType: String, payload: JSONValue, sequence: UInt64, options: EventOptions = EventOptions()) throws -> UniversalEvent {
        let eventID = "event_\(UUID().uuidString.replacingOccurrences(of: "-", with: "").lowercased())"
        let payloadData = try wireJSON(payload)
        var event = UniversalEvent(
            eventID: eventID, taskID: taskID, branchID: options.branchID, accountID: accountID,
            participantID: options.participantID, agentID: options.agentID, providerID: options.providerID,
            deviceID: identity.deviceID, sessionRef: options.sessionRef, parentEventIDs: options.parentEventIDs,
            logicalTime: LogicalTime(wallTimeMS: options.now.epochMilliseconds, counter: 0, node: identity.deviceID),
            deviceSequence: sequence, eventType: eventType, schemaVersion: "1.0", contentType: "application/json",
            contentDigest: "sha256:\(SHA256.hash(data: payloadData).hex)", payload: payload,
            artifactRefs: options.artifactRefs, createdAtClient: options.now.iso8601Milliseconds,
            signature: "", idempotencyKey: eventID, extensions: options.extensions
        )
        let privateKey = try Curve25519.Signing.PrivateKey(rawRepresentation: Data(base64URL: identity.signingPrivateKey))
        event.signature = "ac1.\(try privateKey.signature(for: eventSigningBytes(event)).base64URL)"
        return event
    }

    public static func eventSigningBytes(_ event: UniversalEvent) throws -> Data {
        let fields = [
            "AC-EVENT-1", portable(event.eventID), portable(event.taskID), portable(event.branchID), portable(event.accountID),
            portable(event.participantID ?? ""), portable(event.agentID ?? ""), portable(event.providerID ?? ""), portable(event.deviceID), portable(event.sessionRef ?? ""),
            portable(event.parentEventIDs), String(event.logicalTime.wallTimeMS), String(event.logicalTime.counter), portable(event.logicalTime.node), String(event.deviceSequence),
            portable(event.eventType), portable(event.schemaVersion), portable(event.contentType), portable(event.contentDigest),
            try wireJSON(event.payload).base64URL, portable(event.artifactRefs), String(try epochMilliseconds(event.createdAtClient)), portable(event.idempotencyKey),
            try wireJSON(event.extensions).base64URL,
        ]
        return Data(fields.joined(separator: "\n").utf8)
    }

    public static func signHello(identity: DeviceIdentity, tasks: [SyncTaskState], clientVersion: String = "swift/\(agentContinuitySDKVersion)", now: Date = Date()) throws -> SyncHelloRequest {
        var request = SyncHelloRequest(
            protocolVersions: ["1.0"], capabilities: SyncCapabilities.all, clientVersion: clientVersion,
            deviceID: identity.deviceID, nonce: randomData(count: 24).base64URL, timestamp: now.iso8601Milliseconds, tasks: tasks, proof: ""
        )
        let privateKey = try Curve25519.Signing.PrivateKey(rawRepresentation: Data(base64URL: identity.signingPrivateKey))
        request.proof = "ac1.\(try privateKey.signature(for: helloSigningBytes(request)).base64URL)"
        return request
    }

    public static func helloSigningBytes(_ value: SyncHelloRequest) throws -> Data {
        let tasks = value.tasks.map { SyncTaskState(taskID: $0.taskID, headEventIDs: Array(Set($0.headEventIDs)).sorted(), eventCursor: $0.eventCursor) }.sorted { $0.taskID < $1.taskID }
        var fields = [
            "AC-SYNC-HELLO-1", portable(Array(Set(value.protocolVersions)).sorted()), portable(Array(Set(value.capabilities)).sorted()),
            portable(value.clientVersion), portable(value.deviceID), portable(value.nonce), String(try epochMilliseconds(value.timestamp)), String(tasks.count),
        ]
        for task in tasks { fields.append(contentsOf: [portable(task.taskID), portable(task.headEventIDs), String(task.eventCursor)]) }
        return Data(fields.joined(separator: "\n").utf8)
    }

    private static func portable(_ value: String) -> String { Data(value.utf8).base64URL }
    private static func portable(_ values: [String]) -> String { values.sorted().map(portable).joined(separator: ".") }
    private static func wireJSON(_ value: JSONValue?) throws -> Data {
        guard let value else { return Data() }
        let encoder = JSONEncoder(); encoder.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
        return try encoder.encode(value)
    }
    private static func epochMilliseconds(_ value: String) throws -> Int64 {
        guard let date = parseISO8601(value) else { throw SDKError.invalidDate(value) }
        return date.epochMilliseconds
    }
    private static func randomData(count: Int) -> Data {
        var data = Data(count: count)
        data.withUnsafeMutableBytes { buffer in _ = SecRandomCopyBytes(kSecRandomDefault, count, buffer.baseAddress!) }
        return data
    }
}

public enum SyncCapabilities {
    public static let all = ["batch_push", "cursor_pull", "missing_discovery", "artifact_manifests", "reconciliation", "e2ee"]
}

public struct SyncTaskState: Codable, Equatable, Sendable {
    public var taskID: String
    public var headEventIDs: [String]
    public var eventCursor: UInt64
    public init(taskID: String, headEventIDs: [String], eventCursor: UInt64) { self.taskID = taskID; self.headEventIDs = headEventIDs; self.eventCursor = eventCursor }
    enum CodingKeys: String, CodingKey { case taskID = "task_id", headEventIDs = "head_event_ids", eventCursor = "event_cursor" }
}

public struct SyncHelloRequest: Codable, Sendable {
    public var protocolVersions: [String], capabilities: [String], clientVersion: String, deviceID: String, nonce: String, timestamp: String, tasks: [SyncTaskState], proof: String
    enum CodingKeys: String, CodingKey { case protocolVersions = "protocol_versions", capabilities, clientVersion = "client_version", deviceID = "device_id", nonce, timestamp, tasks, proof }
}

public struct Task: Codable, Sendable {
    public let taskID: String
    public let headEventIDs: [String]
    enum CodingKeys: String, CodingKey { case taskID = "task_id", headEventIDs = "head_event_ids" }
}

public struct SyncTaskPlan: Codable, Sendable {
    public let task: Task
    public let serverCursor: UInt64
    enum CodingKeys: String, CodingKey { case task, serverCursor = "server_cursor" }
}

public struct SyncHelloResponse: Codable, Sendable {
    public let protocolVersion: String
    public let capabilities: [String]
    public let tasks: [SyncTaskPlan]
    enum CodingKeys: String, CodingKey { case protocolVersion = "protocol_version", capabilities, tasks }
}

public struct SyncAcknowledgement: Codable, Sendable {
    public let eventID: String
    public let accepted: Bool
    public let duplicate: Bool
    enum CodingKeys: String, CodingKey { case eventID = "event_id", accepted, duplicate }
}

public struct SyncPushResponse: Codable, Sendable { public let acknowledgements: [SyncAcknowledgement] }

public struct SyncBatch: Codable, Sendable {
    public let task: Task
    public let events: [UniversalEvent]
    public let nextCursor: UInt64
    public let hasMore: Bool
    enum CodingKeys: String, CodingKey { case task, events, nextCursor = "next_cursor", hasMore = "has_more" }
}
public struct SyncPullResponse: Codable, Sendable { public let batches: [SyncBatch] }

public enum SDKError: Error, Equatable { case invalidURL, invalidDate(String), invalidBase64, http(Int, String), emptyResponse }

private struct RefundInput: Encodable { let payment_id: String; let amount_minor: Int64; let reason: String }
public actor AgentContinuityClient {
    public let baseURL: URL
    public var token: String
    private let session: URLSession
    private let encoder: JSONEncoder
    private let decoder: JSONDecoder

    public init(baseURL: URL, token: String = "", session: URLSession = .shared) {
        self.baseURL = baseURL; self.token = token; self.session = session
        self.encoder = JSONEncoder(); self.encoder.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
        self.decoder = JSONDecoder()
    }

    public func registerDevice(_ identity: DeviceIdentity) async throws -> JSONValue {
        try await request("POST", "/v1/devices", body: DeviceRegistration(deviceID: identity.deviceID, name: identity.name, publicKey: identity.signingPublicKey, encryptionPublicKey: identity.encryptionPublicKey))
    }
    public func exchangeSSO(idToken: String) async throws -> JSONValue { try await request("POST", "/v1/auth/sso/exchange", body: TokenInput(idToken: idToken, refreshToken: nil)) }
    public func refreshToken(_ refreshToken: String) async throws -> JSONValue { try await request("POST", "/v1/auth/refresh", body: TokenInput(idToken: nil, refreshToken: refreshToken)) }
    public func logout(refreshToken: String) async throws { let _: EmptyResponse = try await request("POST", "/v1/auth/logout", body: TokenInput(idToken: nil, refreshToken: refreshToken), allowEmpty: true) }
    public func listTasks() async throws -> JSONValue { try await request("GET", "/v1/tasks") }
    public func listStorageJobs(taskID: String, after: Int64 = 0) async throws -> JSONValue { try await request("GET", "/v1/tasks/\(taskID.urlPath)/storage-jobs?after=\(after)") }
    public func getStorageJob(taskID: String, jobID: Int64) async throws -> JSONValue { try await request("GET", "/v1/tasks/\(taskID.urlPath)/storage-jobs/\(jobID)") }
    public func waitStorageJob(taskID: String, jobID: Int64, timeout: TimeInterval = 120) async throws -> JSONValue {
        guard timeout.isFinite && timeout > 0 else { throw SDKError.http(400, "Positive archive wait timeout required") }
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            try _Concurrency.Task<Never, Never>.checkCancellation()
            let job = try await getStorageJob(taskID: taskID, jobID: jobID)
            guard case .object(let fields) = job, case .string(let state) = fields["state"] else { throw SDKError.emptyResponse }
            if state == "stored" { return job }
            if state == "dead" || state == "deleted" { throw SDKError.http(409, "Archive job ended in \(state)") }
            try await _Concurrency.Task<Never, Never>.sleep(nanoseconds: 1_000_000_000)
        }
        throw SDKError.http(408, "Archive receipt timeout; upload may still finish")
    }
    public func retryStorageJob(taskID: String, jobID: Int64) async throws -> JSONValue { try await request("POST", "/v1/tasks/\(taskID.urlPath)/storage-jobs/\(jobID)/retry", body: [String:String]()) }
    /// Acceptance is not cloud completion. Reuse versionID on network retries.
    public func uploadContent(taskID: String, artifactID: String, versionID: String, filename: String, contentType: String, content: Data, parentVersionID: String = "") async throws -> JSONValue {
        guard content.count <= 8 * 1024 * 1024 else { throw SDKError.http(413, "Use resumable upload for content larger than 8 MiB") }
        return try await request("POST", "/v1/tasks/\(taskID.urlPath)/contents", body: ["artifact_id":artifactID,"version_id":versionID,"filename":filename,"content_type":contentType,"content_base64":content.base64EncodedString(),"parent_version_id":parentVersionID])
    }
    public func continuityPackage(taskID: String, targetAgent: String = "generic") async throws -> JSONValue { try await request("GET", "/v1/tasks/\(taskID.urlPath)/continuity-package?agent=\(targetAgent.urlPath)") }
    public func createTask(title: String, objective: String, originDevice: String) async throws -> JSONValue { try await request("POST", "/v1/tasks", body: CreateTask(title: title, objective: objective, originDevice: originDevice)) }
    public func appendEvent(_ event: UniversalEvent) async throws -> JSONValue { try await request("POST", "/v1/tasks/\(event.taskID.urlPath)/events", body: event) }
    public func createAgent(name: String, provider: String, model: String = "", capabilities: [String]) async throws -> JSONValue { try await request("POST", "/v1/agents", body: CreateAgent(name: name, provider: provider, model: model, capabilities: capabilities)) }
    public func bindTaskAgent(taskID: String, agentID: String, resumeMode: String, capabilities: [String], nativeSessionRef: String = "") async throws -> JSONValue { try await request("POST", "/v1/tasks/\(taskID.urlPath)/agents", body: BindAgent(agentID: agentID, role: "contributor", nativeSessionRef: nativeSessionRef, resumeMode: resumeMode, capabilities: capabilities)) }
    public func syncHello(_ input: SyncHelloRequest) async throws -> SyncHelloResponse { try await request("POST", "/v1/sync/hello", body: input) }
    public func syncPush(deviceID: String, events: [UniversalEvent]) async throws -> SyncPushResponse { try await request("POST", "/v1/sync/push", body: SyncPush(deviceID: deviceID, events: events)) }
    public func syncPull(deviceID: String, tasks: [PullCursor]) async throws -> SyncPullResponse { try await request("POST", "/v1/sync/pull", body: SyncPull(deviceID: deviceID, tasks: tasks)) }
    public func syncAck(deviceID: String, tasks: [AckCursor]) async throws { let _: EmptyResponse = try await request("POST", "/v1/sync/ack", body: SyncAck(deviceID: deviceID, tasks: tasks), allowEmpty: true) }

    private func escaped(_ value: String) -> String { value.addingPercentEncoding(withAllowedCharacters: CharacterSet(charactersIn: "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-._~"))! }
    public func createRun(plan: JSONValue, key: String) async throws -> JSONValue { try await request("POST", "/v1/orchestration/runs", body: plan, key: key) }
    public func getRun(_ id: String) async throws -> JSONValue { try await request("GET", "/v1/orchestration/runs/\(escaped(id))") }
    public func controlRun(_ id: String, action: String) async throws -> JSONValue { try await request("POST", "/v1/orchestration/runs/\(escaped(id))/\(escaped(action))") }
    public func issueFiscal(_ id: String, buyer: JSONValue) async throws -> JSONValue { try await request("POST", "/v1/billing/invoices/\(escaped(id))/fiscal", body: buyer) }
    public func getSettlement(_ id: String) async throws -> JSONValue { try await request("GET", "/v1/commerce/settlements/\(escaped(id))") }
    public func listInvoices() async throws -> JSONValue { try await request("GET", "/v1/billing/invoices?limit=200") }
    public func getInvoice(_ id: String) async throws -> JSONValue { try await request("GET", "/v1/billing/invoices/\(escaped(id))") }
    public func quoteInvoice(period: JSONValue) async throws -> JSONValue { try await request("POST", "/v1/billing/quote", body: period) }
    public func issueInvoice(period: JSONValue, key: String) async throws -> JSONValue { try await request("POST", "/v1/billing/invoices", body: period, key: key) }
    public func createPayment(invoiceID: String, provider: String, key: String) async throws -> JSONValue { try await request("POST", "/v1/billing/invoices/\(escaped(invoiceID))/payments", body: ["provider":provider], key: key) }
    public func reconcilePayment(invoiceID: String, paymentID: String) async throws -> JSONValue { try await request("POST", "/v1/billing/invoices/\(escaped(invoiceID))/payments/\(escaped(paymentID))/reconcile") }
    public func requestRefund(invoiceID: String, paymentID: String, amountMinor: Int64, reason: String, key: String) async throws -> JSONValue { try await request("POST", "/v1/billing/invoices/\(escaped(invoiceID))/refunds", body: RefundInput(payment_id: paymentID, amount_minor: amountMinor, reason: reason), key: key) }
    public func reconcileRefund(invoiceID: String, refundID: String) async throws -> JSONValue { try await request("POST", "/v1/billing/invoices/\(escaped(invoiceID))/refunds/\(escaped(refundID))/reconcile") }
    public func invoiceStatement(_ id: String) async throws -> String {
        guard let url = URL(string: "/v1/billing/invoices/\(escaped(id))/statement", relativeTo: baseURL) else { throw SDKError.invalidURL }
        var req = URLRequest(url: url)
        if !token.isEmpty { req.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization") }
        let (data,response) = try await session.data(for: req)
        guard let http = response as? HTTPURLResponse else { throw SDKError.emptyResponse }
        let text = String(data: data, encoding: .utf8) ?? ""
        guard (200..<300).contains(http.statusCode) else { throw SDKError.http(http.statusCode,text) }
        return text
    }

    private func request<Input: Encodable, Output: Decodable>(_ method: String, _ path: String, body: Input, key: String? = nil, allowEmpty: Bool = false) async throws -> Output {
        guard let url = URL(string: path, relativeTo: baseURL) else { throw SDKError.invalidURL }
        var request = URLRequest(url: url); request.httpMethod = method; request.httpBody = try encoder.encode(body)
        if let key { request.setValue(key, forHTTPHeaderField: "Idempotency-Key") }
        request.setValue("application/json", forHTTPHeaderField: "Content-Type"); request.setValue("application/json", forHTTPHeaderField: "Accept")
        request.setValue("agent-continuity-swift/\(agentContinuitySDKVersion)", forHTTPHeaderField: "User-Agent")
        if !token.isEmpty { request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization") }
        let (data, response) = try await session.data(for: request)
        guard let http = response as? HTTPURLResponse else { throw SDKError.emptyResponse }
        guard (200..<300).contains(http.statusCode) else { throw SDKError.http(http.statusCode, String(data: data, encoding: .utf8) ?? "") }
        if data.isEmpty && allowEmpty { return EmptyResponse() as! Output }
        return try decoder.decode(Output.self, from: data)
    }

    private func request<Output: Decodable>(_ method: String, _ path: String) async throws -> Output {
        guard let url = URL(string: path, relativeTo: baseURL) else { throw SDKError.invalidURL }
        var request = URLRequest(url: url); request.httpMethod = method
        request.setValue("application/json", forHTTPHeaderField: "Accept"); request.setValue("agent-continuity-swift/\(agentContinuitySDKVersion)", forHTTPHeaderField: "User-Agent")
        if !token.isEmpty { request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization") }
        let (data, response) = try await session.data(for: request)
        guard let http = response as? HTTPURLResponse else { throw SDKError.emptyResponse }
        guard (200..<300).contains(http.statusCode) else { throw SDKError.http(http.statusCode, String(data: data, encoding: .utf8) ?? "") }
        return try decoder.decode(Output.self, from: data)
    }
}

private struct DeviceRegistration: Codable { let deviceID, name, publicKey, encryptionPublicKey: String; enum CodingKeys: String, CodingKey { case deviceID = "device_id", name, publicKey = "public_key", encryptionPublicKey = "encryption_public_key" } }
private struct CreateTask: Codable { let title, objective, originDevice: String; enum CodingKeys: String, CodingKey { case title, objective, originDevice = "origin_device" } }
private struct CreateAgent: Codable { let name, provider, model: String; let capabilities: [String] }
private struct BindAgent: Codable { let agentID, role, nativeSessionRef, resumeMode: String; let capabilities: [String]; enum CodingKeys: String, CodingKey { case agentID = "agent_id", role, nativeSessionRef = "native_session_ref", resumeMode = "resume_mode", capabilities } }
private struct TokenInput: Codable { let idToken: String?; let refreshToken: String?; enum CodingKeys: String, CodingKey { case idToken = "id_token", refreshToken = "refresh_token" } }
private struct SyncPush: Codable { let deviceID: String; let events: [UniversalEvent]; enum CodingKeys: String, CodingKey { case deviceID = "device_id", events } }
public struct PullCursor: Codable, Sendable { public let taskID: String; public let after: UInt64; public let limit: Int; public let includePayload: Bool; public init(taskID: String, after: UInt64, limit: Int = 200, includePayload: Bool = true) { self.taskID = taskID; self.after = after; self.limit = limit; self.includePayload = includePayload }; enum CodingKeys: String, CodingKey { case taskID = "task_id", after, limit, includePayload = "include_payload" } }
private struct SyncPull: Codable { let deviceID: String; let tasks: [PullCursor]; enum CodingKeys: String, CodingKey { case deviceID = "device_id", tasks } }
public struct AckCursor: Codable, Sendable { public let taskID: String; public let cursor: UInt64; public init(taskID: String, cursor: UInt64) { self.taskID = taskID; self.cursor = cursor }; enum CodingKeys: String, CodingKey { case taskID = "task_id", cursor } }
private struct SyncAck: Codable { let deviceID: String; let tasks: [AckCursor]; enum CodingKeys: String, CodingKey { case deviceID = "device_id", tasks } }
private struct EmptyResponse: Codable { init() {} }

public protocol RuntimeStore: Sendable { func load(key: String) async throws -> Data?; func save(key: String, value: Data) async throws }

public actor MemoryRuntimeStore: RuntimeStore {
    private var values: [String: Data] = [:]
    public init() {}
    public func load(key: String) -> Data? { values[key] }
    public func save(key: String, value: Data) { values[key] = value }
}

public actor FileRuntimeStore: RuntimeStore {
    private let directory: URL
    public init(directory: URL) throws { self.directory = directory; try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true) }
    private func url(_ key: String) -> URL { directory.appendingPathComponent(SHA256.hash(data: Data(key.utf8)).hex + ".json") }
    public func load(key: String) throws -> Data? { try? Data(contentsOf: url(key)) }
    public func save(key: String, value: Data) throws { try value.write(to: url(key), options: [.atomic, .completeFileProtection]) }
}

public struct SyncReport: Equatable, Sendable { public var pushed = 0, duplicates = 0, rejected = 0, pulled = 0, acknowledged = 0 }
private struct LocalTaskState: Codable, Sendable { var cursor: UInt64; var headEventIDs: [String] }
private struct RuntimeState: Codable, Sendable { var sequence: UInt64 = 0; var outbox: [UniversalEvent] = []; var inbox: [UniversalEvent] = []; var tasks: [String: LocalTaskState] = [:] }

public actor DeviceRuntime {
    public let client: AgentContinuityClient
    public let identity: DeviceIdentity
    public let accountID: String
    private let store: any RuntimeStore
    private let key: String
    private let encoder = JSONEncoder()
    private let decoder = JSONDecoder()

    public init(client: AgentContinuityClient, identity: DeviceIdentity, accountID: String, store: any RuntimeStore) {
        self.client = client; self.identity = identity; self.accountID = accountID; self.store = store; self.key = "runtime:\(identity.deviceID)"
        encoder.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
    }

    public func register() async throws -> JSONValue { try await client.registerDevice(identity) }
    public func queueEvent(taskID: String, eventType: String, payload: JSONValue, options initialOptions: EventOptions = EventOptions()) async throws -> UniversalEvent {
        var state = try await load(); state.sequence += 1
        var options = initialOptions
        let local = state.tasks[taskID] ?? LocalTaskState(cursor: 0, headEventIDs: [])
        if options.parentEventIDs.isEmpty { options.parentEventIDs = local.headEventIDs }
        let event = try PortableProtocol.buildSignedEvent(identity: identity, accountID: accountID, taskID: taskID, eventType: eventType, payload: payload, sequence: state.sequence, options: options)
        state.outbox.append(event); state.tasks[taskID] = LocalTaskState(cursor: local.cursor, headEventIDs: [event.eventID]); try await save(state); return event
    }
    public func pendingEvents() async throws -> [UniversalEvent] { try await load().outbox }
    public func receivedEvents() async throws -> [UniversalEvent] { try await load().inbox }

    public func syncOnce() async throws -> SyncReport {
        var state = try await load(); var report = SyncReport()
        if !state.outbox.isEmpty {
            let response = try await client.syncPush(deviceID: identity.deviceID, events: Array(state.outbox.prefix(100)))
            let accepted = Set(response.acknowledgements.filter(\.accepted).map(\.eventID))
            report.pushed = accepted.count; report.duplicates = response.acknowledgements.filter { $0.accepted && $0.duplicate }.count; report.rejected = response.acknowledgements.filter { !$0.accepted }.count
            state.outbox.removeAll { accepted.contains($0.eventID) }
        }
        let taskStates = state.tasks.map { SyncTaskState(taskID: $0.key, headEventIDs: $0.value.headEventIDs, eventCursor: $0.value.cursor) }
        let hello = try await client.syncHello(try PortableProtocol.signHello(identity: identity, tasks: taskStates))
        for plan in hello.tasks {
            var local = state.tasks[plan.task.taskID] ?? LocalTaskState(cursor: 0, headEventIDs: [])
            while local.cursor < plan.serverCursor {
                let response = try await client.syncPull(deviceID: identity.deviceID, tasks: [PullCursor(taskID: plan.task.taskID, after: local.cursor)])
                guard let batch = response.batches.first, batch.nextCursor > local.cursor else { break }
                state.inbox.append(contentsOf: batch.events); local.cursor = batch.nextCursor; local.headEventIDs = batch.task.headEventIDs; report.pulled += batch.events.count
                if !batch.hasMore { break }
            }
            state.tasks[plan.task.taskID] = local
        }
        let acks = state.tasks.compactMap { $0.value.cursor > 0 ? AckCursor(taskID: $0.key, cursor: $0.value.cursor) : nil }
        if !acks.isEmpty { try await client.syncAck(deviceID: identity.deviceID, tasks: acks); report.acknowledged = acks.count }
        try await save(state); return report
    }

    private func load() async throws -> RuntimeState { guard let data = try await store.load(key: key) else { return RuntimeState() }; return try decoder.decode(RuntimeState.self, from: data) }
    private func save(_ value: RuntimeState) async throws { try await store.save(key: key, value: try encoder.encode(value)) }
}

#if canImport(Security)
public struct KeychainIdentityStore: Sendable {
    public let service: String
    public init(service: String = "io.agentcontinuity.sdk") { self.service = service }
    public func save(_ identity: DeviceIdentity, account: String = "device-identity") throws {
        let data = try JSONEncoder().encode(identity)
        let query: [String: Any] = [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: service, kSecAttrAccount as String: account]
        SecItemDelete(query as CFDictionary)
        var insert = query; insert[kSecValueData as String] = data; insert[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        guard SecItemAdd(insert as CFDictionary, nil) == errSecSuccess else { throw SDKError.http(0, "Keychain write failed") }
    }
    public func load(account: String = "device-identity") throws -> DeviceIdentity? {
        let query: [String: Any] = [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: service, kSecAttrAccount as String: account, kSecReturnData as String: true, kSecMatchLimit as String: kSecMatchLimitOne]
        var result: CFTypeRef?; let status = SecItemCopyMatching(query as CFDictionary, &result)
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess, let data = result as? Data else { throw SDKError.http(0, "Keychain read failed") }
        return try JSONDecoder().decode(DeviceIdentity.self, from: data)
    }
}
#endif

public extension Data {
    var base64URL: String { base64EncodedString().replacingOccurrences(of: "+", with: "-").replacingOccurrences(of: "/", with: "_").replacingOccurrences(of: "=", with: "") }
    init(base64URL: String) throws {
        var value = base64URL.replacingOccurrences(of: "-", with: "+").replacingOccurrences(of: "_", with: "/")
        value += String(repeating: "=", count: (4 - value.count % 4) % 4)
        guard let data = Data(base64Encoded: value) else { throw SDKError.invalidBase64 }; self = data
    }
}
private extension Digest { var hex: String { map { String(format: "%02x", $0) }.joined() } }
private extension Date {
    var epochMilliseconds: Int64 { Int64((timeIntervalSince1970 * 1000).rounded(.towardZero)) }
    var iso8601Milliseconds: String { let formatter = ISO8601DateFormatter(); formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]; return formatter.string(from: self) }
}
private extension String { var urlPath: String { addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? self } }

private func parseISO8601(_ value: String) -> Date? {
    let fractional = ISO8601DateFormatter(); fractional.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
    return fractional.date(from: value) ?? ISO8601DateFormatter().date(from: value)
}
