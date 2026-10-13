package table

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

func TaskEvent(ctx context.Context, tx pgx.Tx, id, actor, kind string, detail any) error {
	b, e := json.Marshal(detail)
	if e != nil {
		return e
	}
	_, e = tx.Exec(
		ctx,
		`INSERT INTO coding_events(task_id,actor_id,type,detail) VALUES($1,$2,$3,$4)`,
		id,
		actor,
		kind,
		b,
	)
	return e
}

func TaskEvents(ctx context.Context, db DB, taskID string) ([]map[string]any, error) {
	rows, err := db.Query(
		ctx,
		`SELECT id,actor_id,type,detail,created_at FROM coding_events WHERE task_id=$1 ORDER BY id DESC LIMIT 100`,
		taskID,
	)
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToMap)
	if items == nil {
		items = []map[string]any{}
	}
	return items, err
}
