package table

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

// LockEvaluationEnvironment serializes reconciliation of the same trial.
func LockEvaluationEnvironment(ctx context.Context, db DB, id string) error {
	_, err := db.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "evaluation:"+id)
	return err
}

func InsertEvaluationEnvironment(ctx context.Context, db DB, id string, request, evidence []byte) error {
	_, err := db.Exec(
		ctx,
		`INSERT INTO coding_evaluation_environments(id,request,evidence) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`,
		id,
		request,
		evidence,
	)
	return err
}

type EvaluationEnvironment struct {
	Request  json.RawMessage
	Evidence json.RawMessage
	Artifact json.RawMessage
}

func GetEvaluationEnvironment(ctx context.Context, db DB, id string) (EvaluationEnvironment, error) {
	var environment EvaluationEnvironment
	err := db.QueryRow(ctx, `SELECT request,evidence,artifact FROM coding_evaluation_environments WHERE id=$1`, id).
		Scan(&environment.Request, &environment.Evidence, &environment.Artifact)
	return environment, err
}

func UpdateEvaluationEvidence(ctx context.Context, db DB, id string, evidence []byte) error {
	_, err := db.Exec(
		ctx,
		`UPDATE coding_evaluation_environments SET evidence=$2,updated_at=now() WHERE id=$1`,
		id,
		evidence,
	)
	return err
}

func UpdateEvaluationArtifact(ctx context.Context, db DB, id string, artifact []byte) error {
	_, err := db.Exec(ctx, `UPDATE coding_evaluation_environments SET artifact=$2 WHERE id=$1`, id, artifact)
	return err
}

func ExpiredEvaluationEnvironments(ctx context.Context, db DB) ([]string, error) {
	rows, err := db.Query(
		ctx,
		`SELECT id FROM coding_evaluation_environments WHERE NOT (evidence->>'stopped')::boolean AND created_at < now()-interval '25 hours' LIMIT 100`,
	)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func ExpireEvaluationEnvironment(ctx context.Context, db DB, id string) error {
	_, err := db.Exec(
		ctx,
		`UPDATE coding_evaluation_environments SET evidence=evidence || '{"status":"Failed","stopped":true,"error":"Environment expired"}'::jsonb,updated_at=now() WHERE id=$1`,
		id,
	)
	return err
}
