package coding

import "testing"

func TestMigrationRemovesLegacyExecutorRegistryAndPreservesTasks(t *testing.T) {
	f := setupTask(t)
	_, err := f.a.DB.Exec(f.ctx, `CREATE TABLE coding_executors(worker_id text PRIMARY KEY,gitlab_project_id bigint NOT NULL,last_seen timestamptz NOT NULL); INSERT INTO coding_executors VALUES('legacy-python',77,now())`)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = f.a.Migrate(f.ctx); err != nil {
			t.Fatal(err)
		}
	}
	var registry, task bool
	err = f.a.DB.QueryRow(f.ctx, `SELECT to_regclass('coding_executors') IS NOT NULL,EXISTS(SELECT 1 FROM coding_tasks WHERE id=$1)`, f.task.ID).Scan(&registry, &task)
	if err != nil || registry || !task {
		t.Fatalf("registry=%t task=%t err=%v", registry, task, err)
	}
}
