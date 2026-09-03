package io.agentcontinuity

import java.security.KeyStore
import java.security.spec.AlgorithmParameterSpec
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec
import kotlinx.serialization.encodeToString

/** Host adapter backed by SharedPreferences, DataStore, Room, or an MDM store. */
public interface SecureBlobStore {
    public fun read(key: String): ByteArray?
    public fun write(key: String, value: ByteArray)
}

/**
 * Android-only identity vault that is JVM-compilable and reaches
 * KeyGenParameterSpec through reflection. Private identity data is AES-GCM
 * wrapped by a non-exportable Android Keystore key before reaching BlobStore.
 */
public class AndroidKeystoreIdentityVault(
    private val blobs: SecureBlobStore,
    private val alias: String = "agent-continuity-device-identity",
) {
    private val storageKey = "$alias.ciphertext"

    public fun save(identity: DeviceIdentity) {
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.ENCRYPT_MODE, key())
        val plaintext = protocolJson.encodeToString(identity).toByteArray()
        val ciphertext = cipher.doFinal(plaintext)
        blobs.write(storageKey, byteArrayOf(cipher.iv.size.toByte()) + cipher.iv + ciphertext)
    }

    public fun load(): DeviceIdentity? {
        val envelope = blobs.read(storageKey) ?: return null
        require(envelope.isNotEmpty()) { "invalid Android identity envelope" }
        val ivSize = envelope[0].toInt() and 0xff
        require(ivSize in 12..32 && envelope.size > ivSize + 1) { "invalid Android identity envelope" }
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.DECRYPT_MODE, key(), GCMParameterSpec(128, envelope.copyOfRange(1, 1 + ivSize)))
        return protocolJson.decodeFromString(cipher.doFinal(envelope.copyOfRange(1 + ivSize, envelope.size)).decodeToString())
    }

    private fun key(): SecretKey {
        val store = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        (store.getKey(alias, null) as? SecretKey)?.let { return it }
        val builderClass = Class.forName("android.security.keystore.KeyGenParameterSpec\$Builder")
        val builder = builderClass.getConstructor(String::class.java, Int::class.javaPrimitiveType).newInstance(alias, 3)
        builderClass.getMethod("setBlockModes", Array<String>::class.java).invoke(builder, arrayOf("GCM"))
        builderClass.getMethod("setEncryptionPaddings", Array<String>::class.java).invoke(builder, arrayOf("NoPadding"))
        val specification = builderClass.getMethod("build").invoke(builder) as AlgorithmParameterSpec
        return KeyGenerator.getInstance("AES", "AndroidKeyStore").run { init(specification); generateKey() }
    }
}
