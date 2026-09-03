# Local Deployment

`make test-e2e` launches an isolated Compose project with random credentials, waits for health, runs the end-to-end journey and removes only its own containers, network and volumes.

`make deploy-dev` targets only `rancher-desktop`. The script rejects non-loopback API servers and non-Rancher node names, does not switch the default context, creates `agent-continuity-dev`, preserves any existing secret, and deploys with conservative limits. Run `make smoke-test` afterward. Access the UI with a temporary port-forward; there is no public Ingress by default.

To stop workloads without deleting data, scale the Deployment and StatefulSet to zero in the explicit namespace. Do not delete PVCs unless their contents are disposable or backed up.
