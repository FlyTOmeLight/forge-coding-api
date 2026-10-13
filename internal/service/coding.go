// Package service owns resources and adapts Coding to server-go's lifecycle.
package service

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/FlyTOmeLight/forge-coding-api/internal/app/coding"
	"github.com/FlyTOmeLight/forge-coding-api/internal/client"
	"github.com/FlyTOmeLight/forge-coding-api/internal/client/gitlab"
	"github.com/FlyTOmeLight/forge-coding-api/internal/config"
	"github.com/FlyTOmeLight/forge-coding-api/internal/table"
	"github.com/FlyTOmeLight/forge-coding-api/internal/workspace"
)

type Coding struct {
	cfg        config.Config
	dispatcher bool
	app        *coding.App
	cancel     context.CancelFunc
	done       chan error
}

func New(cfg config.Config, dispatcher bool) *Coding {
	return &Coding{cfg: cfg, dispatcher: dispatcher}
}

func (s *Coding) Initialize(ctx context.Context) error {
	db, err := table.Open(ctx, s.cfg.DatabaseURL)
	if err != nil {
		return err
	}
	h := client.NewHTTP()
	var ws coding.Workspace
	if s.cfg.WorkspaceNamespace != "" {
		ws, err = workspace.New(s.cfg.WorkspaceNamespace, s.cfg.WorkspaceJobImage)
		if err != nil {
			h.CloseIdleConnections()
			db.Close()
			return err
		}
	}
	g, err := gitlab.New(s.cfg.GitLabURL, s.cfg.GitLabToken, s.cfg.GitLabTokenFile, h)
	if err != nil {
		h.CloseIdleConnections()
		db.Close()
		return err
	}
	s.app = coding.New(s.cfg, db, h, ws, g)
	return nil
}

func (s *Coding) Close(ctx context.Context) error {
	if err := s.Stop(ctx); err != nil {
		return err
	}
	if s.app != nil {
		s.app.HTTP.CloseIdleConnections()
		s.app.DB.Close()
	}
	return nil
}

func (s *Coding) RegisterRoutes(router *gin.RouterGroup) error {
	if s.app == nil {
		return errors.New("coding resources are not initialized")
	}
	if err := s.app.RegisterRoutes(router); err != nil {
		return err
	}
	router.GET("/readyz", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if err := s.app.DB.Ping(ctx); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "forge-coding-api"})
	})
	return nil
}

func (s *Coding) Start(ctx context.Context) error {
	if s.app == nil {
		return errors.New("coding resources are not initialized")
	}
	if !s.dispatcher {
		return nil
	}
	ctx, s.cancel = context.WithCancel(ctx)
	s.done = make(chan error, 1)
	go func() { s.done <- s.app.RunDispatcher(ctx); close(s.done) }()
	return nil
}

func (s *Coding) Stop(ctx context.Context) error {
	if s.cancel == nil {
		return nil
	}
	s.cancel()
	select {
	case err := <-s.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RunDispatcher keeps the independently deployed worker in the same lifecycle.
func (s *Coding) RunDispatcher(ctx context.Context) error {
	if err := s.Start(ctx); err != nil {
		return err
	}
	select {
	case err := <-s.done:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		return s.Stop(shutdown)
	}
}

func (s *Coding) Migrate(ctx context.Context) error { return table.Migrate(ctx, s.app.DB) }
