# Agent Continuity .NET SDK

Native .NET 8 client for Windows, Linux, and macOS Agent Apps. It includes Ed25519/X25519 device identity, portable event signing, REST, Agent binding, Continuity Package retrieval, offline runtime state, and cursor sync.

```sh
DOTNET_CLI_TELEMETRY_OPTOUT=1 dotnet run --project tests/AgentContinuity.Tests/AgentContinuity.Tests.csproj
```

Windows production apps should store `DeviceIdentity` through DPAPI or Windows Credential Manager and implement `IRuntimeStore` with SQLite or another transactional store.
