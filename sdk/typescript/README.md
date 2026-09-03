# Agent Continuity TypeScript SDK

Browser, PWA, Electron, and Node.js SDK using WebCrypto. It includes REST, portable device signing, offline runtime state, Agent binding, Continuity Package retrieval, and cursor sync.

```sh
npm ci
npm test
```

Production browser apps should implement `KeyValueStore` with IndexedDB and wrap exported private keys with a platform-bound credential instead of LocalStorage.
