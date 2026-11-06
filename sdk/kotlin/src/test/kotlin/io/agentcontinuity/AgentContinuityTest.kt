package io.agentcontinuity

import java.security.KeyFactory
import java.security.Signature
import java.security.spec.X509EncodedKeySpec
import java.time.Instant
import java.util.Base64
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue
import kotlin.test.assertFailsWith
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import com.sun.net.httpserver.HttpServer
import java.net.InetSocketAddress
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

class AgentContinuityTest {
    @Test fun billingUsesStableKeyAndAuthenticatedStatement() {
        val seen = mutableListOf<String>()
        val server = HttpServer.create(InetSocketAddress("127.0.0.1", 0), 0)
        server.createContext("/") { exchange ->
            val body = exchange.requestBody.bufferedReader().readText()
            seen.add(listOf(exchange.requestURI.rawPath,exchange.requestHeaders.getFirst("Authorization"),exchange.requestHeaders.getFirst("Idempotency-Key"),body).joinToString("|"))
            val bytes = (if(exchange.requestURI.path.endsWith("/statement")) "<html>receipt</html>" else "{\"refunds\":[]}").toByteArray()
            exchange.sendResponseHeaders(200,bytes.size.toLong()); exchange.responseBody.use { it.write(bytes) }
        }
        server.start()
        try {
            val client = AgentContinuityClient("http://127.0.0.1:${server.address.port}","test-token")
            repeat(2) { client.requestRefund("inv/1","pay1",35,"unused","stable-key") }
            assertEquals(seen[0],seen[1]); assertTrue(seen[0].startsWith("/v1/billing/invoices/inv%2F1/refunds|Bearer test-token|stable-key|"))
            assertTrue(seen[0].contains("\"amount_minor\":35"))
            assertEquals("<html>receipt</html>",client.invoiceStatement("inv/1"))
        } finally { server.stop(0) }
    }
    @Test fun waitsForDurableReceiptAndRejectsDeadLetter() {
        val server = HttpServer.create(InetSocketAddress("127.0.0.1", 0), 0)
        server.createContext("/") { exchange ->
            val state = if (exchange.requestURI.path.endsWith("/1")) "stored" else "dead"
            val bytes = "{\"id\":1,\"state\":\"$state\"}".toByteArray()
            exchange.responseHeaders.add("Content-Type", "application/json")
            exchange.sendResponseHeaders(200, bytes.size.toLong())
            exchange.responseBody.use { it.write(bytes) }
        }
        server.start()
        try {
            val client = AgentContinuityClient("http://127.0.0.1:${server.address.port}")
            assertEquals("stored", client.waitStorageJob("task", 1, 1000).jsonObject["state"]!!.jsonPrimitive.content)
            assertFailsWith<IllegalStateException> { client.waitStorageJob("task", 2, 1000) }
        } finally { server.stop(0) }
    }
    @Test fun portableEventIsSignedAndQueued() {
        val identity = DeviceIdentity.generate("Android")
        val event = PortableProtocol.buildSignedEvent(identity, "acct_1", "task_1", "human.message.created", buildJsonObject { put("text", "你好") }, 1u, EventOptions(now = Instant.ofEpochMilli(1_800_000_000_000)))
        assertTrue(event.signature.startsWith("ac1."))
        val publicPrefix = byteArrayOf(0x30, 0x2a, 0x30, 0x05, 0x06, 0x03, 0x2b, 0x65, 0x70, 0x03, 0x21, 0x00)
        val raw = Base64.getUrlDecoder().decode(identity.signingPublicKey)
        val publicKey = KeyFactory.getInstance("Ed25519").generatePublic(X509EncodedKeySpec(publicPrefix + raw))
        val verified = Signature.getInstance("Ed25519").run { initVerify(publicKey); update(PortableProtocol.eventSigningBytes(event)); verify(Base64.getUrlDecoder().decode(event.signature.removePrefix("ac1."))) }
        assertTrue(verified)

        val runtime = DeviceRuntime(AgentContinuityClient("http://127.0.0.1:18080"), identity, "acct_1", MemoryRuntimeStore())
        runtime.queueEvent("task_1", "progress.updated", buildJsonObject { put("percent", 20) })
        assertEquals(1, runtime.pendingEvents().size)
    }
}
