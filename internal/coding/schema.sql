CREATE TABLE IF NOT EXISTS schema_versions (version integer PRIMARY KEY);
CREATE TABLE IF NOT EXISTS coding_tasks (
 id text PRIMARY KEY, tenant_id text NOT NULL, project_id text NOT NULL,
 owner_id text NOT NULL, title text NOT NULL, description text NOT NULL, criteria text NOT NULL,
 branch text NOT NULL, target_branch text NOT NULL, mr_iid bigint NOT NULL CHECK(mr_iid>0),
 head text NOT NULL, status text NOT NULL DEFAULT 'Review', control text NOT NULL DEFAULT 'Human',
 version bigint NOT NULL DEFAULT 1, epoch bigint NOT NULL DEFAULT 1, round integer NOT NULL DEFAULT 0,
 checks text NOT NULL DEFAULT 'NotRun', checked_head text NOT NULL DEFAULT '', approval_head text NOT NULL DEFAULT '',
 reviewer_ids text[] NOT NULL, active_run text, stop_requested_at timestamptz,
 stop_confirmed boolean NOT NULL DEFAULT true, discussions jsonb NOT NULL DEFAULT '[]',
 synthetic boolean NOT NULL DEFAULT false, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(project_id,mr_iid)
);
CREATE TABLE IF NOT EXISTS coding_runs (
 id text PRIMARY KEY, task_id text NOT NULL REFERENCES coding_tasks(id), epoch bigint NOT NULL,
 head text NOT NULL, status text NOT NULL DEFAULT 'Queued', requirements text NOT NULL,
 claimed_by text NOT NULL DEFAULT '', lease_until timestamptz, output jsonb NOT NULL DEFAULT '{}',
 created_at timestamptz NOT NULL DEFAULT now(), finished_at timestamptz
);
CREATE UNIQUE INDEX IF NOT EXISTS coding_one_active_run ON coding_runs(task_id) WHERE status IN ('Queued','Running','Stopping');
CREATE TABLE IF NOT EXISTS coding_events (
 id bigserial PRIMARY KEY, task_id text NOT NULL REFERENCES coding_tasks(id), actor_id text NOT NULL,
 type text NOT NULL, detail jsonb NOT NULL DEFAULT '{}', created_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO schema_versions(version) VALUES(3) ON CONFLICT DO NOTHING;
CREATE TABLE IF NOT EXISTS coding_feedback (
 id text PRIMARY KEY, task_id text NOT NULL REFERENCES coding_tasks(id), submitted_by text NOT NULL,
 head text NOT NULL, requirements text NOT NULL, status text NOT NULL DEFAULT 'Pending', created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE coding_tasks DROP CONSTRAINT IF EXISTS coding_tasks_mr_iid_check;
ALTER TABLE coding_tasks ADD CONSTRAINT coding_tasks_mr_iid_check CHECK(mr_iid>=0);
ALTER TABLE coding_tasks DROP CONSTRAINT IF EXISTS coding_tasks_project_id_mr_iid_key;
CREATE UNIQUE INDEX IF NOT EXISTS coding_one_mr ON coding_tasks(project_id,mr_iid) WHERE mr_iid>0;
ALTER TABLE coding_tasks ADD COLUMN IF NOT EXISTS base_branch text NOT NULL DEFAULT '';
ALTER TABLE coding_tasks ADD COLUMN IF NOT EXISTS reproduce text NOT NULL DEFAULT '';
ALTER TABLE coding_tasks ADD COLUMN IF NOT EXISTS constraints text NOT NULL DEFAULT '';
ALTER TABLE coding_tasks ADD COLUMN IF NOT EXISTS reference text NOT NULL DEFAULT '';
ALTER TABLE coding_tasks ADD COLUMN IF NOT EXISTS request_key text NOT NULL DEFAULT '';
ALTER TABLE coding_tasks ADD COLUMN IF NOT EXISTS request_hash text NOT NULL DEFAULT '';
CREATE UNIQUE INDEX IF NOT EXISTS coding_task_request ON coding_tasks(request_key) WHERE request_key<>'';
ALTER TABLE coding_runs ADD COLUMN IF NOT EXISTS config_snapshot jsonb NOT NULL DEFAULT '{}';
CREATE TABLE IF NOT EXISTS coding_project_settings (
 project_id text PRIMARY KEY,tenant_id text NOT NULL,version bigint NOT NULL DEFAULT 1,
 settings jsonb NOT NULL,checked_at timestamptz
);
CREATE TABLE IF NOT EXISTS coding_executors (worker_id text PRIMARY KEY,gitlab_project_id bigint NOT NULL,last_seen timestamptz NOT NULL);
INSERT INTO schema_versions(version) VALUES(4) ON CONFLICT DO NOTHING;
ALTER TABLE coding_tasks ADD COLUMN IF NOT EXISTS agent_judgment jsonb NOT NULL DEFAULT '{"status":"Unknown"}';

ALTER TABLE coding_runs ADD COLUMN IF NOT EXISTS runtime_id text;
ALTER TABLE coding_runs ADD COLUMN IF NOT EXISTS execution_snapshot jsonb;
CREATE UNIQUE INDEX IF NOT EXISTS coding_runtime_run ON coding_runs(runtime_id) WHERE runtime_id IS NOT NULL;
