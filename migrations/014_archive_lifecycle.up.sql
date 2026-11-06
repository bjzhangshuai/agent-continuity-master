BEGIN;
ALTER TABLE archive_jobs DROP CONSTRAINT archive_jobs_state_check;
ALTER TABLE archive_jobs ADD CONSTRAINT archive_jobs_state_check CHECK (state IN ('queued','processing','stored','retry','dead','deleted'));
ALTER TABLE archive_jobs ADD COLUMN operation text NOT NULL DEFAULT 'archive' CHECK (operation IN ('archive','delete'));
ALTER TABLE archive_jobs ADD COLUMN stored_at timestamptz;
ALTER TABLE archive_jobs ADD COLUMN deleted_at timestamptz;
ALTER TABLE archive_jobs ADD COLUMN retain_until timestamptz;
ALTER TABLE archive_jobs ADD COLUMN legal_hold boolean NOT NULL DEFAULT false;
ALTER TABLE archive_jobs ADD COLUMN cache_evicted_at timestamptz;
ALTER TABLE archive_jobs ADD COLUMN cache_released_at timestamptz;
UPDATE archive_jobs SET stored_at=updated_at WHERE state='stored';
CREATE INDEX archive_jobs_retained_idx ON archive_jobs(organization_id,account_id,stored_at) WHERE stored_at IS NOT NULL;
CREATE TABLE archive_action_audit (
 action_id bigserial PRIMARY KEY,
 job_id bigint NOT NULL REFERENCES archive_jobs(job_id),
 actor text NOT NULL,
 action text NOT NULL,
 reason text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
-- Deleted jobs are terminal, not pending work. Keep source + outbox atomic.
CREATE OR REPLACE FUNCTION continuity_archive_enqueue(kind text, task text, resource text, version text, ref text, mime text, hash text, bytes bigint)
RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE config archive_control%ROWTYPE; org text; owner_account text; target text; result bigint;
BEGIN
 SELECT * INTO config FROM archive_control WHERE singleton;
 IF NOT config.enabled THEN RETURN NULL; END IF;
 SELECT job_id INTO result FROM archive_jobs WHERE task_id=task AND source_kind=kind AND resource_id=resource AND version_id=version;
 IF result IS NOT NULL THEN RETURN result; END IF;
 IF (SELECT count(*) FROM (SELECT 1 FROM archive_jobs WHERE state NOT IN ('stored','deleted') LIMIT config.max_pending) q) >= config.max_pending THEN
   RAISE EXCEPTION 'archive_queue_full' USING ERRCODE='P0001';
 END IF;
 SELECT COALESCE(organization_id,''),account_id INTO org,owner_account FROM tasks WHERE task_id=task;
 target := COALESCE(config.organization_targets->>org, config.default_target);
 INSERT INTO archive_jobs(task_id,organization_id,account_id,source_kind,resource_id,version_id,target_id,source_ref,content_type,content_hash,size)
 VALUES(task,org,owner_account,kind,resource,version,target,ref,mime,hash,bytes)
 ON CONFLICT(task_id,source_kind,resource_id,version_id) DO NOTHING RETURNING job_id INTO result;
 RETURN result;
END $$;
INSERT INTO schema_migrations(version) VALUES('014_archive_lifecycle') ON CONFLICT DO NOTHING;
COMMIT;
