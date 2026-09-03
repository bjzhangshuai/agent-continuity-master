package io.agentcontinuity

import java.security.KeyFactory
import java.security.Signature
import java.security.spec.X509EncodedKeySpec
import java.time.Instant
import java.util.Base64
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

class AgentContinuityTest {
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
