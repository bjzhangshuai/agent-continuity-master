# Development

Requirements are Go 1.26.7+, Docker/Rancher Desktop for integration, Helm 3 and Kubernetes 1.24+. Run `make bootstrap`, `make fmt`, `make lint`, `make test`, `make test-race`, `make test-e2e`, and `make helm-lint`.

For a no-dependency server, export two random secrets and run `make dev`:

```sh
export CONTINUITY_JWT_SECRET="$(openssl rand -hex 32)"
export CONTINUITY_BOOTSTRAP_TOKEN="$(openssl rand -hex 32)"
export CONTINUITY_MASTER_KEY="$(openssl rand -base64 32 | tr '/+' '_-' | tr -d '=')"
make dev
```

Never use a real tenant database for tests. `CONTINUITY_TEST_DATABASE_URL` is opt-in and the test refuses unrecognized database names. Preserve unrelated working-tree changes and add an ADR when changing protocol, persistence, cryptography or deployment topology.
