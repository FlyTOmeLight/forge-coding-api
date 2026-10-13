package service

import (
	"context"
	"net/http"

	"github.com/FlyTOmeLight/server-go/app"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FlyTOmeLight/forge-coding-api/internal/client"
	"github.com/FlyTOmeLight/forge-coding-api/internal/table"
)

type Config struct {
	Coding CodingConfig
}

// Build gives standalone resources to app.Run before constructing Coding.
// Combined hosts call New with their shared resources instead.
func Build(ctx context.Context, runtime *app.Runtime, cfg *Config) error {
	c, err := cfg.Coding.Validate()
	if err != nil {
		return err
	}
	r := &resources{databaseURL: c.DatabaseURL}
	if err := runtime.Initialize(ctx, r); err != nil {
		return err
	}
	svc, err := New(c, r.db, r.http, false)
	if err != nil {
		return err
	}
	return runtime.AddService(svc)
}

// resources owns only connections; services must finish before Close.
type resources struct {
	databaseURL string
	db          *pgxpool.Pool
	http        *http.Client
}

func (r *resources) Initialize(ctx context.Context) error {
	db, err := table.Open(ctx, r.databaseURL)
	if err != nil {
		return err
	}
	r.db = db
	r.http = client.NewHTTP()
	return nil
}

func (r *resources) Close(context.Context) error {
	if r.http != nil {
		r.http.CloseIdleConnections()
	}
	if r.db != nil {
		r.db.Close()
	}
	return nil
}
