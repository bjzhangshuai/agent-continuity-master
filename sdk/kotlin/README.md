# Agent Continuity Kotlin SDK

Kotlin/JVM 17 library compatible with Android Agent Apps. It includes REST, portable Ed25519/X25519 identity, signed events, Agent binding, Continuity Package retrieval, crash-persisted cursor sync, and an Android Keystore identity vault that does not require Android classes at JVM compile time.

```sh
./gradlew test
./gradlew publishToMavenLocal
```

Implement `SecureBlobStore` with Android DataStore/Room and pass it to `AndroidKeystoreIdentityVault`. Implement `RuntimeStore` with Room for production background sync.
