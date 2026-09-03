CREATE TABLE agents (
    agent_id text PRIMARY KEY,
    account_id text NOT NULL REFERENCES accounts(account_id) ON DELETE CASCADE,
    name text NOT NULL,
    provider text NOT NULL,
    model text,
    capabilities jsonb NOT NULL DEFAULT '[]'::jsonb,
    card bytea,
    created_at timestamptz NOT NULL,
    revoked_at timestamptz
);
CREATE INDEX agents_account_idx ON agents(account_id, created_at DESC);

CREATE TABLE task_participants (
    participant_id text PRIMARY KEY,
    task_id text NOT NULL REFERENCES tasks(task_id) ON DELETE CASCADE,
    account_id text REFERENCES accounts(account_id) ON DELETE CASCADE,
    agent_id text REFERENCES agents(agent_id) ON DELETE CASCADE,
    kind text NOT NULL CHECK (kind IN ('human','agent','service')),
    role text NOT NULL CHECK (role IN ('viewer','contributor','owner','reviewer')),
    status text NOT NULL CHECK (status IN ('active','left','revoked')),
    joined_at timestamptz NOT NULL,
    left_at timestamptz,
    CHECK ((kind='human' AND account_id IS NOT NULL AND agent_id IS NULL) OR (kind<>'human' AND agent_id IS NOT NULL))
);
CREATE UNIQUE INDEX task_participants_human_unique ON task_participants(task_id,account_id) WHERE account_id IS NOT NULL;
CREATE UNIQUE INDEX task_participants_agent_unique ON task_participants(task_id,agent_id) WHERE agent_id IS NOT NULL;

INSERT INTO task_participants(participant_id,task_id,account_id,kind,role,status,joined_at)
SELECT 'participant_owner_' || task_id,task_id,account_id,'human','owner','active',created_at FROM tasks
ON CONFLICT DO NOTHING;

CREATE TABLE task_agent_bindings (
    task_id text NOT NULL REFERENCES tasks(task_id) ON DELETE CASCADE,
    agent_id text NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    participant_id text NOT NULL REFERENCES task_participants(participant_id) ON DELETE CASCADE,
    native_session_ref text,
    resume_mode text NOT NULL CHECK (resume_mode IN ('native','rehydrate','unsupported')),
    capabilities jsonb NOT NULL DEFAULT '[]'::jsonb,
    metadata bytea,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (task_id, agent_id)
);

CREATE TABLE notifications (
    notification_id text PRIMARY KEY,
    account_id text NOT NULL REFERENCES accounts(account_id) ON DELETE CASCADE,
    task_id text REFERENCES tasks(task_id) ON DELETE CASCADE,
    kind text NOT NULL,
    actor_id text,
    subject text NOT NULL,
    details bytea,
    created_at timestamptz NOT NULL,
    read_at timestamptz
);
CREATE INDEX notifications_unread_idx ON notifications(account_id, read_at, created_at DESC);

INSERT INTO schema_migrations(version) VALUES ('005_collaboration') ON CONFLICT DO NOTHING;
