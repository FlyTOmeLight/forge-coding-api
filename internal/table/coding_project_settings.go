package table

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

type ProjectSettings struct {
	Version   int64
	Settings  json.RawMessage
	CheckedAt *time.Time
}

func GetProjectSettings(ctx context.Context, db DB, tid, pid string, locked bool) (ProjectSettings, error) {
	var out ProjectSettings
	sql := `SELECT version,settings,checked_at FROM coding_project_settings WHERE tenant_id=$1 AND project_id=$2`
	if locked {
		sql += ` FOR UPDATE`
	}
	err := db.QueryRow(ctx, sql, tid, pid).Scan(&out.Version, &out.Settings, &out.CheckedAt)
	return out, err
}

func LockProjectSettings(ctx context.Context, tx pgx.Tx, pid string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "coding-settings:"+pid)
	return err
}

func SaveProjectSettings(ctx context.Context, tx pgx.Tx, tid, pid string, raw []byte) error {
	_, err := tx.Exec(
		ctx,
		`INSERT INTO coding_project_settings(tenant_id,project_id,version,settings) VALUES($1,$2,1,$3) ON CONFLICT(project_id) DO UPDATE SET version=coding_project_settings.version+1,settings=EXCLUDED.settings,checked_at=NULL`,
		tid,
		pid,
		raw,
	)
	return err
}

func MarkProjectSettingsChecked(
	ctx context.Context,
	db DB,
	tid, pid string,
	version int64,
	now time.Time,
) (bool, error) {
	tag, err := db.Exec(
		ctx,
		`UPDATE coding_project_settings SET checked_at=$4 WHERE tenant_id=$1 AND project_id=$2 AND version=$3`,
		tid,
		pid,
		version,
		now,
	)
	return tag.RowsAffected() == 1, err
}

func UpdateReleasedProjectSettings(ctx context.Context, tx pgx.Tx, tid, pid string, raw []byte, now time.Time) error {
	_, err := tx.Exec(
		ctx,
		`UPDATE coding_project_settings SET settings=$3,version=version+1,checked_at=$4 WHERE tenant_id=$1 AND project_id=$2`,
		tid,
		pid,
		raw,
		now,
	)
	return err
}
