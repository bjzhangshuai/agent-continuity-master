-- A durable outbox is committed in the SAME transaction as the immutable source.
-- Disabled by default; no historical data is exported until explicit backfill.
BEGIN;
CREATE TABLE archive_targets (target_id text PRIMARY KEY, identity_hash text NOT NULL);
CREATE TABLE archive_control (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton), enabled boolean NOT NULL DEFAULT false,
 default_target text NOT NULL DEFAULT '', organization_targets jsonb NOT NULL DEFAULT '{}',
 max_pending integer NOT NULL DEFAULT 10000 CHECK(max_pending>0)
);
INSERT INTO archive_control(singleton) VALUES(true);
CREATE TABLE archive_jobs (
 job_id bigserial PRIMARY KEY, task_id text NOT NULL REFERENCES tasks(task_id) ON DELETE CASCADE,
 organization_id text NOT NULL DEFAULT '', account_id text NOT NULL REFERENCES accounts(account_id),
 source_kind text NOT NULL CHECK(source_kind IN ('event','artifact')), resource_id text NOT NULL,
 version_id text NOT NULL DEFAULT '', target_id text NOT NULL REFERENCES archive_targets(target_id),
 state text NOT NULL DEFAULT 'queued' CHECK(state IN ('queued','processing','retry','stored','dead')),
 source_ref text NOT NULL DEFAULT '', content_type text NOT NULL, content_hash text NOT NULL DEFAULT '',
 size bigint NOT NULL DEFAULT 0 CHECK(size>=0), object_key text NOT NULL DEFAULT '',
 attempts integer NOT NULL DEFAULT 0, lease_version bigint NOT NULL DEFAULT 0, lease_until timestamptz, dispatched_at timestamptz,
 error_code text NOT NULL DEFAULT '', next_attempt timestamptz NOT NULL DEFAULT now(),
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(task_id,source_kind,resource_id,version_id)
);
CREATE INDEX archive_jobs_claim_idx ON archive_jobs(next_attempt,job_id) WHERE state IN ('queued','retry','processing');
CREATE INDEX archive_jobs_task_idx ON archive_jobs(task_id,job_id);
CREATE INDEX archive_jobs_ref_idx ON archive_jobs(task_id,resource_id,version_id) WHERE source_kind='artifact';

CREATE FUNCTION continuity_archive_enqueue(kind text, task text, resource text, version text, ref text, mime text, hash text, bytes bigint)
RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE config archive_control%ROWTYPE; org text; owner_account text; target text; result bigint;
BEGIN
 SELECT * INTO config FROM archive_control WHERE singleton;
 IF NOT config.enabled THEN RETURN NULL; END IF;
 -- Duplicate retries must succeed even when the queue is full.
 SELECT job_id INTO result FROM archive_jobs WHERE task_id=task AND source_kind=kind AND resource_id=resource AND version_id=version;
 IF result IS NOT NULL THEN RETURN result; END IF;
 IF (SELECT count(*) FROM (SELECT 1 FROM archive_jobs WHERE state <> 'stored' LIMIT config.max_pending) q) >= config.max_pending THEN
   RAISE EXCEPTION 'archive_queue_full' USING ERRCODE='P0001';
 END IF;
 SELECT COALESCE(organization_id,''),account_id INTO org,owner_account FROM tasks WHERE task_id=task;
 target := COALESCE(config.organization_targets->>org, config.default_target);
 INSERT INTO archive_jobs(task_id,organization_id,account_id,source_kind,resource_id,version_id,target_id,source_ref,content_type,content_hash,size)
 VALUES(task,org,owner_account,kind,resource,version,target,ref,mime,hash,bytes)
 ON CONFLICT(task_id,source_kind,resource_id,version_id) DO NOTHING RETURNING job_id INTO result;
 RETURN result;
END $$;
CREATE FUNCTION continuity_archive_event() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM continuity_archive_enqueue('event',NEW.task_id,NEW.event_id,'','','application/json','',0);
 RETURN NEW;
END $$;
CREATE FUNCTION continuity_archive_artifact() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM continuity_archive_enqueue('artifact',NEW.task_id,NEW.artifact_id,NEW.version_id,NEW.storage_reference,NEW.content_type,NEW.content_hash,NEW.size);
 RETURN NEW;
END $$;
CREATE TRIGGER event_archive_outbox AFTER INSERT ON events FOR EACH ROW EXECUTE FUNCTION continuity_archive_event();
CREATE TRIGGER artifact_archive_outbox AFTER INSERT ON artifact_versions FOR EACH ROW EXECUTE FUNCTION continuity_archive_artifact();
INSERT INTO schema_migrations(version) VALUES('013_object_archive') ON CONFLICT DO NOTHING;
COMMIT;
