// import-tenant moves the existing coding tables into an empty Coding database.
// Stop both applications before running; the source is retained as a rollback copy.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/FlyTOmeLight/forge-coding-api/internal/table"
)

func main() {
	if e := run(); e != nil {
		slog.Error("coding import failed", "error", e)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	source, e := pgx.Connect(ctx, os.Getenv("SOURCE_DATABASE_URL"))
	if e != nil {
		return errors.New("connect source database")
	}
	defer source.Close(ctx)
	db, e := table.Open(ctx, os.Getenv("DATABASE_URL"))
	if e != nil {
		return e
	}
	defer db.Close()
	if e = table.Migrate(ctx, db); e != nil {
		return e
	}
	from, e := source.Begin(ctx)
	if e != nil {
		return e
	}
	defer from.Rollback(ctx)
	to, e := db.Begin(ctx)
	if e != nil {
		return e
	}
	defer to.Rollback(ctx)
	tables := []string{"coding_tasks", "coding_runs", "coding_events", "coding_feedback", "coding_project_settings"}
	for _, table := range tables {
		if _, e = from.Exec(ctx, "LOCK TABLE "+pgx.Identifier{table}.Sanitize()+" IN SHARE MODE"); e != nil {
			return e
		}
	}
	var active int
	if e = from.QueryRow(ctx, `SELECT count(*) FROM coding_runs WHERE status IN ('Queued','Running')`).
		Scan(&active); e != nil {
		return e
	}
	if active > 0 {
		return fmt.Errorf("%d source rounds are active; stop and confirm them before migration", active)
	}
	counts := map[string]int64{}
	for _, table := range tables {
		var n int
		if e = to.QueryRow(ctx, "SELECT count(*) FROM "+pgx.Identifier{table}.Sanitize()).Scan(&n); e != nil {
			return e
		}
		if n != 0 {
			return fmt.Errorf("target table %s must be empty", table)
		}
		rows, e := from.Query(ctx, "SELECT * FROM "+pgx.Identifier{table}.Sanitize())
		if e != nil {
			return e
		}
		fields := rows.FieldDescriptions()
		cols := make([]string, len(fields))
		for i, f := range fields {
			cols[i] = f.Name
		}
		copied, e := to.CopyFrom(ctx, pgx.Identifier{table}, cols, rows)
		rows.Close()
		if e != nil {
			return e
		}
		counts[table] = copied
	}
	if _, e = to.Exec(
		ctx,
		`SELECT setval(pg_get_serial_sequence('coding_events','id'),coalesce((SELECT max(id) FROM coding_events),1),EXISTS(SELECT 1 FROM coding_events))`,
	); e != nil {
		return e
	}
	if e = to.Commit(ctx); e != nil {
		return e
	}
	if e = from.Commit(ctx); e != nil {
		return e
	}
	for _, table := range tables {
		slog.Info("imported coding records", "table", table, "rows", counts[table])
	}
	return nil
}
