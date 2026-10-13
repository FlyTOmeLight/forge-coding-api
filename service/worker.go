package service

import (
	"context"
	"errors"
	"time"

	server "github.com/FlyTOmeLight/server-go"
	"github.com/FlyTOmeLight/server-go/conf"
	"github.com/FlyTOmeLight/server-go/logger"

	"github.com/FlyTOmeLight/forge-coding-api/internal/table"
)

// RunDispatcher runs the independently deployed worker without an HTTP host.
func RunDispatcher(ctx context.Context, configPath string) error {
	return runOffline(ctx, configPath, false)
}

// Migrate applies the database schema without starting workers or HTTP.
func Migrate(ctx context.Context, configPath string) error {
	return runOffline(ctx, configPath, true)
}

func runOffline(ctx context.Context, configPath string, migrate bool) (result error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	manager, err := conf.NewManager(configPath)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, manager.Close(context.WithoutCancel(ctx))) }()
	var cfg Config
	if err := manager.Snapshot().Unmarshal(&cfg); err != nil {
		return err
	}
	c, err := cfg.Coding.Validate()
	if err != nil {
		return err
	}
	log, err := logger.NewLogger(&logger.Config{Level: "info", Outputs: []logger.Output{{Type: "console"}}})
	if err != nil {
		return err
	}
	defer log.Close()
	ctx = logger.WithContext(ctx, log)
	r := &resources{databaseURL: c.DatabaseURL}
	bootstrap := server.NewBootstrap(r)
	initCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	err = bootstrap.Initialize(initCtx)
	cancel()
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		result = errors.Join(result, bootstrap.Close(closeCtx))
	}()
	if migrate {
		return table.Migrate(ctx, r.db)
	}
	svc, err := New(c, r.db, r.http, true)
	if err != nil {
		return err
	}
	if err := svc.Start(ctx); err != nil {
		return err
	}
	select {
	case <-svc.done:
	case <-ctx.Done():
	}
	stopCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	err = svc.Stop(stopCtx)
	stop()
	if err != nil {
		// Never release connections while a task still uses them.
		err = errors.Join(err, svc.Stop(context.WithoutCancel(ctx)))
	}
	return err
}
