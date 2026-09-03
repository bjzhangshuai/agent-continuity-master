# Rollback

Application rollback is `make rollback`, guarded to the local Rancher context. Production rollback must specify an approved context and previous Helm revision. Event/API migrations are additive so the previous compatible application can run.

Never automatically run `*.down.sql`: it deletes durable state. If a migration is faulty, stop rollout, keep the expanded schema, redeploy the previous compatible image, and restore only after a documented data-integrity decision. PVCs are kept by Helm.
