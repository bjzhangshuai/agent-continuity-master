CREATE TABLE task_branches (
    task_id text NOT NULL REFERENCES tasks(task_id) ON DELETE CASCADE,
    branch_id text NOT NULL,
    name text NOT NULL,
    base_event_id text,
    head_event_ids jsonb NOT NULL DEFAULT '[]'::jsonb,
    status text NOT NULL CHECK (status IN ('active','merged','archived')),
    created_by text NOT NULL REFERENCES accounts(account_id),
    created_at timestamptz NOT NULL,
    merged_into text,
    merged_at timestamptz,
    PRIMARY KEY (task_id, branch_id)
);
CREATE INDEX task_branches_activity_idx ON task_branches(task_id, status, created_at);

INSERT INTO task_branches(task_id,branch_id,name,head_event_ids,status,created_by,created_at)
SELECT task_id,'main','Main',head_event_ids,'active',account_id,created_at FROM tasks
ON CONFLICT DO NOTHING;

CREATE TABLE task_snapshots (
    snapshot_id text PRIMARY KEY,
    task_id text NOT NULL REFERENCES tasks(task_id) ON DELETE CASCADE,
    branch_id text NOT NULL,
    through_server_sequence bigint NOT NULL CHECK (through_server_sequence >= 0),
    event_count integer NOT NULL CHECK (event_count >= 0),
    state bytea NOT NULL,
    state_digest text NOT NULL,
    created_at timestamptz NOT NULL,
    UNIQUE (task_id, branch_id, through_server_sequence),
    FOREIGN KEY (task_id, branch_id) REFERENCES task_branches(task_id, branch_id) ON DELETE CASCADE
);
CREATE INDEX task_snapshots_latest_idx ON task_snapshots(task_id, branch_id, through_server_sequence DESC);

CREATE TABLE conflict_resolutions (
    resolution_id text PRIMARY KEY,
    conflict_id text NOT NULL UNIQUE REFERENCES conflicts(conflict_id) ON DELETE CASCADE,
    task_id text NOT NULL REFERENCES tasks(task_id) ON DELETE CASCADE,
    account_id text NOT NULL REFERENCES accounts(account_id),
    strategy text NOT NULL,
    resolution_event_id text REFERENCES events(event_id),
    notes text,
    created_at timestamptz NOT NULL
);

INSERT INTO schema_migrations(version) VALUES ('003_task_dag') ON CONFLICT DO NOTHING;
