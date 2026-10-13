package table

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func ReconfirmFeedback(ctx context.Context, db DB, feedbackID, taskID, head string) (pgconn.CommandTag, error) {
	return db.Exec(
		ctx,
		`UPDATE coding_feedback SET head=$3,status='Pending' WHERE id=$1 AND task_id=$2 AND status IN ('Pending','NeedsReconfirmation')`,
		feedbackID,
		taskID,
		head,
	)
}

func CreateFeedback(
	ctx context.Context,
	db DB,
	feedbackID, taskID, actorID, head, requirements string,
) (pgconn.CommandTag, error) {
	return db.Exec(
		ctx,
		`INSERT INTO coding_feedback(id,task_id,submitted_by,head,requirements) VALUES($1,$2,$3,$4,$5)`,
		feedbackID,
		taskID,
		actorID,
		head,
		requirements,
	)
}

func LockNextFeedback(ctx context.Context, db DB, taskID, feedbackID string) (FeedbackRecord, error) {
	var row FeedbackRecord
	err := db.QueryRow(ctx, `SELECT id,head,status,requirements FROM coding_feedback WHERE task_id=$1 AND status IN ('Pending','NeedsReconfirmation') AND ($2='' OR id=$2) ORDER BY created_at,id LIMIT 1 FOR UPDATE`, taskID, feedbackID).
		Scan(&row.ID, &row.Head, &row.Status, &row.Requirements)
	return row, err
}

func ConsumeFeedback(ctx context.Context, db DB, feedbackID string) (pgconn.CommandTag, error) {
	return db.Exec(ctx, `UPDATE coding_feedback SET status='Consumed' WHERE id=$1`, feedbackID)
}

func InvalidateFeedback(ctx context.Context, db DB, taskID, head string) (pgconn.CommandTag, error) {
	return db.Exec(
		ctx,
		`UPDATE coding_feedback SET status='NeedsReconfirmation' WHERE task_id=$1 AND status='Pending' AND head<>$2`,
		taskID,
		head,
	)
}

type FeedbackRecord struct {
	ID           string
	Head         string
	Status       string
	Requirements string
}

func TaskFeedback(ctx context.Context, db DB, taskID string) ([]map[string]any, error) {
	rows, err := db.Query(
		ctx,
		`SELECT id,submitted_by,head,requirements,status,created_at FROM coding_feedback WHERE task_id=$1 ORDER BY created_at,id LIMIT 100`,
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
