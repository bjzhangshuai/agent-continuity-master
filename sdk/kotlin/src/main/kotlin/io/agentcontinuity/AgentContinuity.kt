package io.agentcontinuity

import java.io.File
import java.net.HttpURLConnection
import java.net.URI
import java.net.URLEncoder
import java.nio.charset.StandardCharsets
import java.security.KeyFactory
import java.security.KeyPairGenerator
import java.security.MessageDigest
import java.security.PrivateKey
import java.security.Signature
import java.security.spec.PKCS8EncodedKeySpec
import java.time.Instant
import java.util.Base64
import java.util.UUID
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.encodeToString
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonArray
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.decodeFromJsonElement
import kotlinx.serialization.json.encodeToJsonElement
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.put

public const val SDK_VERSION: String = "0.1.0"
private const val SIGNATURE_PREFIX = "ac1."

public val protocolJson: Json = Json {
    ignoreUnknownKeys = true
    encodeDefaults = true
    explicitNulls = false
}

@Serializable
public data class DeviceIdentity(
    @SerialName("device_id") val deviceId: String,
    val name: String,
    @SerialName("signing_private_key") val signingPrivateKey: String,
    @SerialName("signing_public_key") val signingPublicKey: String,
    @SerialName("encryption_private_key") val encryptionPrivateKey: String,
    @SerialName("encryption_public_key") val encryptionPublicKey: String,
) {
    public companion object {
        public fun generate(name: String, deviceId: String = ""): DeviceIdentity {
            val signing = KeyPairGenerator.getInstance("Ed25519").generateKeyPair()
            val encryption = KeyPairGenerator.getInstance("X25519").generateKeyPair()
            return DeviceIdentity(
                deviceId.ifEmpty { "device_${UUID.randomUUID().toString().replace("-", "")}" }, name,
                b64(signing.private.encoded), b64(rawPublicKey(signing.public.encoded)),
                b64(encryption.private.encoded), b64(rawPublicKey(encryption.public.encoded)),
            )
        }
    }
}

@Serializable
public data class LogicalTime(
    @SerialName("wall_time_ms") val wallTimeMs: Long,
    val counter: UInt,
    val node: String,
)

@Serializable
public data class UniversalEvent(
    @SerialName("event_id") val eventId: String,
    @SerialName("task_id") val taskId: String,
    @SerialName("branch_id") val branchId: String,
    @SerialName("account_id") val accountId: String,
    @SerialName("participant_id") val participantId: String? = null,
    @SerialName("agent_id") val agentId: String? = null,
    @SerialName("provider_id") val providerId: String? = null,
    @SerialName("device_id") val deviceId: String,
    @SerialName("session_ref") val sessionRef: String? = null,
    @SerialName("parent_event_ids") val parentEventIds: List<String>,
    @SerialName("logical_time") val logicalTime: LogicalTime,
    @SerialName("device_sequence") val deviceSequence: ULong,
    @SerialName("event_type") val eventType: String,
    @SerialName("schema_version") val schemaVersion: String = "1.0",
    @SerialName("content_type") val contentType: String = "application/json",
    @SerialName("content_digest") val contentDigest: String,
    val payload: JsonElement? = null,
    @SerialName("artifact_refs") val artifactRefs: List<String> = emptyList(),
    @SerialName("created_at_client") val createdAtClient: String,
    val signature: String,
    @SerialName("idempotency_key") val idempotencyKey: String,
    val extensions: JsonElement? = null,
)

public data class EventOptions(
    val branchId: String = "main",
    val parentEventIds: List<String> = emptyList(),
    val participantId: String? = null,
    val agentId: String? = null,
    val providerId: String? = null,
    val sessionRef: String? = null,
    val artifactRefs: List<String> = emptyList(),
    val extensions: JsonElement? = null,
    val now: Instant = Instant.now(),
)

public object PortableProtocol {
    public fun buildSignedEvent(identity: DeviceIdentity, accountId: String, taskId: String, eventType: String, payload: JsonElement, sequence: ULong, options: EventOptions = EventOptions()): UniversalEvent {
        val eventId = "event_${UUID.randomUUID().toString().replace("-", "")}"
        val payloadBytes = wireJson(payload)
        val unsigned = UniversalEvent(
            eventId = eventId, taskId = taskId, branchId = options.branchId, accountId = accountId,
            participantId = options.participantId, agentId = options.agentId, providerId = options.providerId,
            deviceId = identity.deviceId, sessionRef = options.sessionRef, parentEventIds = options.parentEventIds,
            logicalTime = LogicalTime(options.now.toEpochMilli(), 0u, identity.deviceId), deviceSequence = sequence,
            eventType = eventType, contentDigest = "sha256:${sha256(payloadBytes).hex()}", payload = payload,
            artifactRefs = options.artifactRefs, createdAtClient = options.now.toString(), signature = "", idempotencyKey = eventId, extensions = options.extensions,
        )
        return unsigned.copy(signature = SIGNATURE_PREFIX + b64(sign(signingPrivate(identity), eventSigningBytes(unsigned))))
    }

    public fun eventSigningBytes(event: UniversalEvent): ByteArray = listOf(
        "AC-EVENT-1", portable(event.eventId), portable(event.taskId), portable(event.branchId), portable(event.accountId),
        portable(event.participantId.orEmpty()), portable(event.agentId.orEmpty()), portable(event.providerId.orEmpty()), portable(event.deviceId), portable(event.sessionRef.orEmpty()),
        portable(event.parentEventIds), event.logicalTime.wallTimeMs.toString(), event.logicalTime.counter.toString(), portable(event.logicalTime.node), event.deviceSequence.toString(),
        portable(event.eventType), portable(event.schemaVersion), portable(event.contentType), portable(event.contentDigest),
        b64(wireJson(event.payload)), portable(event.artifactRefs), Instant.parse(event.createdAtClient).toEpochMilli().toString(), portable(event.idempotencyKey), b64(wireJson(event.extensions)),
    ).joinToString("\n").toByteArray()

    public fun signHello(identity: DeviceIdentity, tasks: List<SyncTaskState>, clientVersion: String = "kotlin/$SDK_VERSION", now: Instant = Instant.now()): SyncHelloRequest {
        val unsigned = SyncHelloRequest(
            protocolVersions = listOf("1.0"), capabilities = SyncCapabilities.all, clientVersion = clientVersion,
            deviceId = identity.deviceId, nonce = b64(randomBytes(24)), timestamp = now.toString(), tasks = tasks, proof = "",
        )
        return unsigned.copy(proof = SIGNATURE_PREFIX + b64(sign(signingPrivate(identity), helloSigningBytes(unsigned))))
    }

    public fun helloSigningBytes(value: SyncHelloRequest): ByteArray {
        val tasks = value.tasks.map { it.copy(headEventIds = it.headEventIds.distinct().sorted()) }.sortedBy { it.taskId }
        val fields = mutableListOf(
            "AC-SYNC-HELLO-1", portable(value.protocolVersions.distinct()), portable(value.capabilities.distinct()),
            portable(value.clientVersion), portable(value.deviceId), portable(value.nonce), Instant.parse(value.timestamp).toEpochMilli().toString(), tasks.size.toString(),
        )
        tasks.forEach { fields += listOf(portable(it.taskId), portable(it.headEventIds), it.eventCursor.toString()) }
        return fields.joinToString("\n").toByteArray()
    }
}

public object SyncCapabilities {
    public val all: List<String> = listOf("batch_push", "cursor_pull", "missing_discovery", "artifact_manifests", "reconciliation", "e2ee")
}

@Serializable
public data class SyncTaskState(
    @SerialName("task_id") val taskId: String,
    @SerialName("head_event_ids") val headEventIds: List<String>,
    @SerialName("event_cursor") val eventCursor: ULong,
)

@Serializable
public data class SyncHelloRequest(
    @SerialName("protocol_versions") val protocolVersions: List<String>,
    val capabilities: List<String>,
    @SerialName("client_version") val clientVersion: String,
    @SerialName("device_id") val deviceId: String,
    val nonce: String,
    val timestamp: String,
    val tasks: List<SyncTaskState>,
    val proof: String,
)

@Serializable public data class TaskSummary(@SerialName("task_id") val taskId: String, @SerialName("head_event_ids") val headEventIds: List<String> = emptyList())
@Serializable public data class SyncTaskPlan(val task: TaskSummary, @SerialName("server_cursor") val serverCursor: ULong)
@Serializable public data class SyncHelloResponse(@SerialName("protocol_version") val protocolVersion: String, val capabilities: List<String>, val tasks: List<SyncTaskPlan>)
@Serializable public data class SyncAcknowledgement(@SerialName("event_id") val eventId: String, val accepted: Boolean, val duplicate: Boolean = false)
@Serializable public data class SyncPushResponse(val acknowledgements: List<SyncAcknowledgement>)
@Serializable public data class PullCursor(@SerialName("task_id") val taskId: String, val after: ULong, val limit: Int = 200, @SerialName("include_payload") val includePayload: Boolean = true)
@Serializable public data class SyncBatch(val task: TaskSummary, val events: List<UniversalEvent>, @SerialName("next_cursor") val nextCursor: ULong, @SerialName("has_more") val hasMore: Boolean)
@Serializable public data class SyncPullResponse(val batches: List<SyncBatch>)
@Serializable public data class AckCursor(@SerialName("task_id") val taskId: String, val cursor: ULong)

public class APIException(public val status: Int, public val code: String, message: String) : RuntimeException("API $status $code: $message")

public class AgentContinuityClient(public val baseUrl: String, public var token: String = "", public val timeoutMillis: Int = 30_000) {
    public fun listInvoices(): JsonElement = requestJson("GET", "/v1/billing/invoices?limit=200")
    public fun getInvoice(id: String): JsonElement = requestJson("GET", "/v1/billing/invoices/${path(id)}")
    public fun quoteInvoice(period: JsonObject): JsonElement = requestJson("POST", "/v1/billing/quote", period)
    public fun issueInvoice(period: JsonObject, key: String): JsonElement = requestJson("POST", "/v1/billing/invoices", period, mapOf("Idempotency-Key" to key))
    public fun createPayment(id: String, provider: String, key: String): JsonElement = requestJson("POST", "/v1/billing/invoices/${path(id)}/payments", buildJsonObject { put("provider", provider) }, mapOf("Idempotency-Key" to key))
    public fun reconcilePayment(id: String, payment: String): JsonElement = requestJson("POST", "/v1/billing/invoices/${path(id)}/payments/${path(payment)}/reconcile")
    public fun requestRefund(id: String, payment: String, amountMinor: Long, reason: String, key: String): JsonElement = requestJson("POST", "/v1/billing/invoices/${path(id)}/refunds", buildJsonObject { put("payment_id", payment); put("amount_minor", amountMinor); put("reason", reason) }, mapOf("Idempotency-Key" to key))
    public fun reconcileRefund(id: String, refund: String): JsonElement = requestJson("POST", "/v1/billing/invoices/${path(id)}/refunds/${path(refund)}/reconcile")
    public fun invoiceStatement(id: String): String = requestRaw("GET", "/v1/billing/invoices/${path(id)}/statement")
    public fun listStorageJobs(taskId: String, after: Long = 0): JsonElement = requestJson("GET", "/v1/tasks/${path(taskId)}/storage-jobs?after=$after")
    public fun getStorageJob(taskId: String, jobId: Long): JsonElement = requestJson("GET", "/v1/tasks/${path(taskId)}/storage-jobs/$jobId")
    /** Blocking IO: call on a background dispatcher, never Android's UI thread. */
    public fun waitStorageJob(taskId: String, jobId: Long, timeoutMillis: Long = 120000): JsonElement {
        require(timeoutMillis in 1..86400000) { "Archive wait timeout must be 1ms..24h" }
        val started = System.nanoTime()
        while ((System.nanoTime() - started) / 1000000 < timeoutMillis) {
            if (Thread.currentThread().isInterrupted) throw InterruptedException("Archive wait canceled")
            val job = getStorageJob(taskId, jobId)
            val state = job.jsonObject["state"]?.jsonPrimitive?.content
            if (state == "stored") return job
            check(state != "dead" && state != "deleted") { "Archive job ended in $state" }
            Thread.sleep(1000)
        }
        throw java.util.concurrent.TimeoutException("Archive receipt timeout; upload may still finish")
    }
    public fun retryStorageJob(taskId: String, jobId: Long): JsonElement = requestJson("POST", "/v1/tasks/${path(taskId)}/storage-jobs/$jobId/retry")
    public fun uploadContent(taskId: String, artifactId: String, versionId: String, filename: String, contentType: String, content: ByteArray, parentVersionId: String = ""): JsonElement {
        require(content.size <= 8 * 1024 * 1024) { "Use resumable upload for content larger than 8 MiB" }
        return requestJson("POST", "/v1/tasks/${path(taskId)}/contents", JsonObject(mapOf("artifact_id" to JsonPrimitive(artifactId), "version_id" to JsonPrimitive(versionId), "filename" to JsonPrimitive(filename), "content_type" to JsonPrimitive(contentType), "content_base64" to JsonPrimitive(Base64.getEncoder().encodeToString(content)), "parent_version_id" to JsonPrimitive(parentVersionId))))
    }
    public fun exchangeSso(idToken: String): JsonObject = requestJson("POST", "/v1/auth/sso/exchange", buildJsonObject { put("id_token", idToken) }).jsonObject
    public fun refreshToken(refreshToken: String): JsonObject = requestJson("POST", "/v1/auth/refresh", buildJsonObject { put("refresh_token", refreshToken) }).jsonObject
    public fun logout(refreshToken: String): Unit { requestJson("POST", "/v1/auth/logout", buildJsonObject { put("refresh_token", refreshToken) }) }
    public fun registerDevice(identity: DeviceIdentity): JsonObject = requestJson("POST", "/v1/devices", buildJsonObject {
        put("device_id", identity.deviceId); put("name", identity.name); put("public_key", identity.signingPublicKey); put("encryption_public_key", identity.encryptionPublicKey)
    }).jsonObject

    public fun createTask(title: String, objective: String, originDevice: String): JsonObject = requestJson("POST", "/v1/tasks", buildJsonObject { put("title", title); put("objective", objective); put("origin_device", originDevice) }).jsonObject
    public fun appendEvent(event: UniversalEvent): JsonObject = requestJson("POST", "/v1/tasks/${path(event.taskId)}/events", protocolJson.encodeToJsonElement(event)).jsonObject
    public fun createAgent(name: String, provider: String, capabilities: List<String>, model: String = ""): JsonObject = requestJson("POST", "/v1/agents", buildJsonObject { put("name", name); put("provider", provider); put("model", model); put("capabilities", buildJsonArray { capabilities.forEach { add(JsonPrimitive(it)) } }) }).jsonObject
    public fun bindTaskAgent(taskId: String, agentId: String, resumeMode: String, capabilities: List<String>, nativeSessionRef: String = ""): JsonObject = requestJson("POST", "/v1/tasks/${path(taskId)}/agents", buildJsonObject { put("agent_id", agentId); put("role", "contributor"); put("native_session_ref", nativeSessionRef); put("resume_mode", resumeMode); put("capabilities", buildJsonArray { capabilities.forEach { add(JsonPrimitive(it)) } }) }).jsonObject
    public fun listTasks(): JsonArray = requestJson("GET", "/v1/tasks").jsonObject["tasks"]!!.jsonArray
    public fun listDevices(): JsonArray = requestJson("GET", "/v1/devices").jsonObject["devices"]!!.jsonArray
    public fun listAgents(): JsonArray = requestJson("GET", "/v1/agents").jsonObject["agents"]!!.jsonArray
    public fun continuityPackage(taskId: String, agent: String = "generic"): JsonObject = requestJson("GET", "/v1/tasks/${path(taskId)}/continuity-package?agent=${path(agent)}").jsonObject
    public fun syncHello(value: SyncHelloRequest): SyncHelloResponse = protocolJson.decodeFromJsonElement(requestJson("POST", "/v1/sync/hello", protocolJson.encodeToJsonElement(value)))
    public fun syncPush(deviceId: String, events: List<UniversalEvent>): SyncPushResponse = protocolJson.decodeFromJsonElement(requestJson("POST", "/v1/sync/push", buildJsonObject { put("device_id", deviceId); put("events", protocolJson.encodeToJsonElement(events)) }))
    public fun syncPull(deviceId: String, tasks: List<PullCursor>): SyncPullResponse = protocolJson.decodeFromJsonElement(requestJson("POST", "/v1/sync/pull", buildJsonObject { put("device_id", deviceId); put("tasks", protocolJson.encodeToJsonElement(tasks)) }))
    public fun syncAck(deviceId: String, tasks: List<AckCursor>): Unit { requestRaw("POST", "/v1/sync/ack", buildJsonObject { put("device_id", deviceId); put("tasks", protocolJson.encodeToJsonElement(tasks)) }) }

    public fun requestJson(method: String, route: String, body: JsonElement? = null, headers: Map<String,String> = emptyMap()): JsonElement {
        val raw = requestRaw(method, route, body, headers)
        return if (raw.isBlank()) JsonObject(emptyMap()) else protocolJson.parseToJsonElement(raw)
    }

    private fun requestRaw(method: String, route: String, body: JsonElement? = null, headers: Map<String,String> = emptyMap()): String {
        val connection = URI(baseUrl.trimEnd('/') + route).toURL().openConnection() as HttpURLConnection
        connection.requestMethod = method; connection.connectTimeout = timeoutMillis; connection.readTimeout = timeoutMillis
        connection.setRequestProperty("Accept", "application/json"); connection.setRequestProperty("User-Agent", "agent-continuity-kotlin/$SDK_VERSION")
        headers.forEach { (key, value) -> connection.setRequestProperty(key, value) }
        if (token.isNotEmpty()) connection.setRequestProperty("Authorization", "Bearer $token")
        if (body != null) {
            connection.doOutput = true; connection.setRequestProperty("Content-Type", "application/json")
            connection.outputStream.use { it.write(body.toString().toByteArray()) }
        }
        val status = connection.responseCode
        val text = (if (status in 200..299) connection.inputStream else connection.errorStream)?.bufferedReader()?.use { it.readText() }.orEmpty()
        if (status !in 200..299) {
            val error = runCatching { protocolJson.parseToJsonElement(text).jsonObject["error"]!!.jsonObject }.getOrNull()
            throw APIException(status, error?.get("code")?.jsonPrimitive?.content ?: "http_error", error?.get("message")?.jsonPrimitive?.content ?: connection.responseMessage)
        }
        return text
    }
    private fun path(value: String): String = URLEncoder.encode(value, StandardCharsets.UTF_8).replace("+", "%20")
}

public interface RuntimeStore { public fun load(key: String): String?; public fun save(key: String, value: String) }
public class MemoryRuntimeStore : RuntimeStore {
    private val values = mutableMapOf<String, String>()
    override fun load(key: String): String? = synchronized(values) { values[key] }
    override fun save(key: String, value: String): Unit = synchronized(values) { values[key] = value }
}
public class FileRuntimeStore(private val directory: File) : RuntimeStore {
    init { directory.mkdirs(); require(directory.isDirectory) }
    private fun file(key: String) = File(directory, sha256(key.toByteArray()).hex() + ".json")
    override fun load(key: String): String? = file(key).takeIf(File::exists)?.readText()
    override fun save(key: String, value: String) { val temporary = File.createTempFile(".state-", ".tmp", directory); temporary.writeText(value); check(temporary.renameTo(file(key)) || run { temporary.copyTo(file(key), overwrite = true); temporary.delete() }) }
}

@Serializable private data class LocalTaskState(var cursor: ULong = 0u, @SerialName("head_event_ids") var headEventIds: List<String> = emptyList())
@Serializable private data class RuntimeState(var sequence: ULong = 0u, val outbox: MutableList<UniversalEvent> = mutableListOf(), val inbox: MutableList<UniversalEvent> = mutableListOf(), val tasks: MutableMap<String, LocalTaskState> = mutableMapOf())
public data class SyncReport(var pushed: Int = 0, var duplicates: Int = 0, var rejected: Int = 0, var pulled: Int = 0, var acknowledged: Int = 0)

public class DeviceRuntime(public val client: AgentContinuityClient, public val identity: DeviceIdentity, public val accountId: String, private val store: RuntimeStore) {
    private val key = "runtime:${identity.deviceId}"
    public fun register(): JsonObject = client.registerDevice(identity)
    @Synchronized public fun queueEvent(taskId: String, eventType: String, payload: JsonElement, options: EventOptions = EventOptions()): UniversalEvent {
        val state = load(); state.sequence++
        val local = state.tasks.getOrPut(taskId) { LocalTaskState() }
        val effective = if (options.parentEventIds.isEmpty()) options.copy(parentEventIds = local.headEventIds) else options
        val event = PortableProtocol.buildSignedEvent(identity, accountId, taskId, eventType, payload, state.sequence, effective)
        state.outbox += event; local.headEventIds = listOf(event.eventId); save(state); return event
    }
    public fun pendingEvents(): List<UniversalEvent> = load().outbox
    public fun receivedEvents(): List<UniversalEvent> = load().inbox
    @Synchronized public fun syncOnce(): SyncReport {
        val state = load(); val report = SyncReport()
        if (state.outbox.isNotEmpty()) {
            val response = client.syncPush(identity.deviceId, state.outbox.take(100)); val accepted = response.acknowledgements.filter { it.accepted }.map { it.eventId }.toSet()
            report.pushed = accepted.size; report.duplicates = response.acknowledgements.count { it.accepted && it.duplicate }; report.rejected = response.acknowledgements.count { !it.accepted }; state.outbox.removeAll { it.eventId in accepted }
        }
        val taskStates = state.tasks.map { SyncTaskState(it.key, it.value.headEventIds, it.value.cursor) }
        val hello = client.syncHello(PortableProtocol.signHello(identity, taskStates))
        hello.tasks.forEach { plan ->
            val local = state.tasks.getOrPut(plan.task.taskId) { LocalTaskState() }
            while (local.cursor < plan.serverCursor) {
                val batch = client.syncPull(identity.deviceId, listOf(PullCursor(plan.task.taskId, local.cursor))).batches.firstOrNull() ?: break
                if (batch.nextCursor <= local.cursor) break
                state.inbox += batch.events; local.cursor = batch.nextCursor; local.headEventIds = batch.task.headEventIds; report.pulled += batch.events.size
                if (!batch.hasMore) break
            }
        }
        val acks = state.tasks.filterValues { it.cursor > 0u }.map { AckCursor(it.key, it.value.cursor) }
        if (acks.isNotEmpty()) { client.syncAck(identity.deviceId, acks); report.acknowledged = acks.size }
        save(state); return report
    }
    private fun load(): RuntimeState = store.load(key)?.let { protocolJson.decodeFromString<RuntimeState>(it) } ?: RuntimeState()
    private fun save(state: RuntimeState) = store.save(key, protocolJson.encodeToString(state))
}

private fun b64(value: ByteArray): String = Base64.getUrlEncoder().withoutPadding().encodeToString(value)
private fun rawPublicKey(encoded: ByteArray): ByteArray { require(encoded.size >= 32); return encoded.copyOfRange(encoded.size - 32, encoded.size) }
private fun signingPrivate(identity: DeviceIdentity): PrivateKey = KeyFactory.getInstance("Ed25519").generatePrivate(PKCS8EncodedKeySpec(Base64.getUrlDecoder().decode(identity.signingPrivateKey)))
private fun sign(key: PrivateKey, value: ByteArray): ByteArray = Signature.getInstance("Ed25519").run { initSign(key); update(value); sign() }
private fun randomBytes(size: Int): ByteArray = ByteArray(size).also(java.security.SecureRandom()::nextBytes)
private fun sha256(value: ByteArray): ByteArray = MessageDigest.getInstance("SHA-256").digest(value)
private fun ByteArray.hex(): String = joinToString("") { "%02x".format(it) }
private fun portable(value: String): String = b64(value.toByteArray())
private fun portable(values: List<String>): String = values.sorted().joinToString(".", transform = ::portable)
private fun wireJson(value: JsonElement?): ByteArray = value?.toString()?.toByteArray() ?: ByteArray(0)
