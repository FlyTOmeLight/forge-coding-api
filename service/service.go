// Package service assembles Coding and owns its framework lifecycle.
package service

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/FlyTOmeLight/server-go/host"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FlyTOmeLight/forge-coding-api/internal/app/coding"
	"github.com/FlyTOmeLight/forge-coding-api/internal/client/gitlab"
	"github.com/FlyTOmeLight/forge-coding-api/internal/config"
	"github.com/FlyTOmeLight/forge-coding-api/internal/workspace"
)

// CodingConfig holds Coding's configuration and business validation rules.
type CodingConfig = config.Config

type Coding struct {
	app        *coding.App
	dispatcher bool
	cancel     context.CancelFunc
	done       chan struct{}
	runErr     error
}

var _ host.Service = (*Coding)(nil)

// New borrows database and HTTP connections from the caller. Stop only stops
// Coding's tasks; the caller closes shared resources after all users stop.
// cfg must already contain validated configuration. dispatcher enables workers
// for a combined host; the standalone HTTP Build leaves them disabled.
func New(cfg CodingConfig, database *pgxpool.Pool, httpClient *http.Client, dispatcher bool) (*Coding, error) {
	if database == nil || httpClient == nil {
		return nil, errors.New("coding requires a database and HTTP client")
	}
	var ws coding.Workspace
	var err error
	if cfg.WorkspaceNamespace != "" {
		ws, err = workspace.New(cfg.WorkspaceNamespace, cfg.WorkspaceJobImage)
		if err != nil {
			return nil, err
		}
	}
	g, err := gitlab.New(cfg.GitLabURL, cfg.GitLabToken, cfg.GitLabTokenFile, httpClient)
	if err != nil {
		return nil, err
	}
	return &Coding{app: coding.New(cfg, database, httpClient, ws, g), dispatcher: dispatcher}, nil
}

func (s *Coding) RegisterRoutes(router *core.RouterGroup) error {
	if err := s.app.RegisterRoutes(router); err != nil {
		return err
	}
	router.GET("/readyz", func(c *core.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if err := s.app.DB.Ping(ctx); err != nil {
			c.JSON(http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
		c.JSON(http.StatusOK, map[string]string{"status": "ok", "service": "forge-coding-api"})
	})
	return nil
}

// Start and Stop are serialized by the host. A failed Stop can be retried.
func (s *Coding) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !s.dispatcher {
		return nil
	}
	if s.done != nil {
		return errors.New("coding dispatcher already started")
	}
	ctx, s.cancel = context.WithCancel(ctx)
	s.done = make(chan struct{})
	go func() {
		defer close(s.done)
		s.runErr = s.app.RunDispatcher(ctx)
	}()
	return nil
}

func (s *Coding) Stop(ctx context.Context) error {
	if s.cancel == nil {
		return nil
	}
	s.cancel()
	select {
	case <-s.done:
		return s.runErr
	case <-ctx.Done():
		return ctx.Err()
	}
}
