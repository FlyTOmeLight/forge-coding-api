package table

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

type ProjectRelease struct {
	ID                 string
	ActorID            string
	ActorName          string
	Action             string
	RestoredFrom       string
	SourceAgentID      string
	SourceAgentVersion int64
	ExperimentID       string
	SuiteDigest        string
	Settings           json.RawMessage
	CreatedAt          time.Time
}

const projectReleaseColumns = `id,actor_id,actor_name,action,restored_from,source_agent_id,source_agent_version,experiment_id,suite_digest,settings,created_at`

func scanProjectRelease(row pgx.Row) (ProjectRelease, error) {
	var out ProjectRelease
	err := row.Scan(
		&out.ID,
		&out.ActorID,
		&out.ActorName,
		&out.Action,
		&out.RestoredFrom,
		&out.SourceAgentID,
		&out.SourceAgentVersion,
		&out.ExperimentID,
		&out.SuiteDigest,
		&out.Settings,
		&out.CreatedAt,
	)
	return out, err
}

func ListProjectReleases(ctx context.Context, db DB, tid, pid string) ([]ProjectRelease, error) {
	rows, err := db.Query(
		ctx,
		`SELECT `+projectReleaseColumns+` FROM coding_project_releases WHERE tenant_id=$1 AND project_id=$2 ORDER BY created_at DESC,id DESC LIMIT 100`,
		tid,
		pid,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProjectRelease{}
	for rows.Next() {
		item, err := scanProjectRelease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func ProjectReleaseRequestHash(ctx context.Context, db DB, tid, pid, key string) (string, error) {
	var hash string
	err := db.QueryRow(ctx, `SELECT request_hash FROM coding_project_releases WHERE tenant_id=$1 AND project_id=$2 AND request_key=$3`, tid, pid, key).
		Scan(&hash)
	return hash, err
}

func ProjectReleaseByRequest(ctx context.Context, db DB, tid, pid, key string) (ProjectRelease, error) {
	return scanProjectRelease(
		db.QueryRow(
			ctx,
			`SELECT `+projectReleaseColumns+` FROM coding_project_releases WHERE tenant_id=$1 AND project_id=$2 AND request_key=$3`,
			tid,
			pid,
			key,
		),
	)
}

func ProjectReleaseByID(ctx context.Context, db DB, tid, pid, id string) (ProjectRelease, error) {
	return scanProjectRelease(
		db.QueryRow(
			ctx,
			`SELECT `+projectReleaseColumns+` FROM coding_project_releases WHERE tenant_id=$1 AND project_id=$2 AND id=$3`,
			tid,
			pid,
			id,
		),
	)
}

func InsertProjectRelease(
	ctx context.Context,
	tx pgx.Tx,
	tid, pid, key, hash string,
	item ProjectRelease,
) (ProjectRelease, error) {
	return scanProjectRelease(
		tx.QueryRow(
			ctx,
			`INSERT INTO coding_project_releases(id,tenant_id,project_id,request_key,request_hash,actor_id,actor_name,action,restored_from,source_agent_id,source_agent_version,experiment_id,suite_digest,settings) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) RETURNING `+projectReleaseColumns,
			item.ID,
			tid,
			pid,
			key,
			hash,
			item.ActorID,
			item.ActorName,
			item.Action,
			item.RestoredFrom,
			item.SourceAgentID,
			item.SourceAgentVersion,
			item.ExperimentID,
			item.SuiteDigest,
			item.Settings,
		),
	)
}
