CREATE TABLE IF NOT EXISTS workflow_operations (
 kind text NOT NULL, operation_id text NOT NULL, owner_id text NOT NULL,
 updated_at timestamptz NOT NULL, resource_id text NOT NULL DEFAULT '',
 peer_id text NOT NULL DEFAULT '', status text NOT NULL, version bigint NOT NULL CHECK(version>0),
 due_at timestamptz NOT NULL, document jsonb NOT NULL,
 PRIMARY KEY(kind,operation_id)
);
CREATE INDEX IF NOT EXISTS operations_owner ON workflow_operations(kind,owner_id,operation_id);
CREATE INDEX IF NOT EXISTS operations_peer ON workflow_operations(kind,peer_id,operation_id);
CREATE INDEX IF NOT EXISTS operations_due ON workflow_operations(kind,due_at) WHERE status IN ('queued','running','pending');

CREATE INDEX IF NOT EXISTS operations_run_cursor ON workflow_operations(owner_id,updated_at DESC,operation_id DESC) WHERE kind='run';
CREATE INDEX IF NOT EXISTS operations_run_task ON workflow_operations(owner_id,resource_id,updated_at DESC) WHERE kind='run';
