// Package service assembles Coding and owns its framework lifecycle.
package service

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/FlyTOmeLight/server-go/auth"
	"github.com/FlyTOmeLight/server-go/host"
	"github.com/FlyTOmeLight/server-go/logger"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"github.com/FlyTOmeLight/forge-coding-api/internal/app/access"
	"github.com/FlyTOmeLight/forge-coding-api/internal/app/evaluation"
	"github.com/FlyTOmeLight/forge-coding-api/internal/app/platform"
	"github.com/FlyTOmeLight/forge-coding-api/internal/app/project"
	"github.com/FlyTOmeLight/forge-coding-api/internal/app/task"
	"github.com/FlyTOmeLight/forge-coding-api/internal/client/gitlab"
	"github.com/FlyTOmeLight/forge-coding-api/internal/config"
	"github.com/FlyTOmeLight/forge-coding-api/internal/httpapi"
	"github.com/FlyTOmeLight/forge-coding-api/internal/workspace"
)

// CodingConfig holds Coding's configuration and business validation rules.
type CodingConfig = config.Config

type Coding struct {
	database             *pgxpool.Pool
	tasks                *task.Service
	evaluations          *evaluation.Service
	taskController       *task.Controller
	projectController    *project.Controller
	evaluationController *evaluation.Controller
	platformController   *platform.Controller
	dispatcher           bool
	cancel               context.CancelFunc
	done                 chan struct{}
	runErr               error
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
	var ws workspace.Provider
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
	permissions := access.New(cfg, httpClient)
	projects := project.New(cfg, database, httpClient, g, permissions)
	tasks := task.New(cfg, database, httpClient, ws, g, projects, permissions)
	evaluations := evaluation.New(cfg, database, httpClient, ws, projects, permissions)
	return &Coding{
		database: database, tasks: tasks, evaluations: evaluations, dispatcher: dispatcher,
		taskController: task.NewController(tasks), projectController: project.NewController(projects),
		evaluationController: evaluation.NewController(evaluations),
		platformController:   platform.NewController(platform.New(cfg, httpClient, g), permissions),
	}, nil
}

func (s *Coding) RegisterRoutes(router *core.RouterGroup) error {
	router.Use(httpapi.PathValues)
	internal := router.Group(
		"/internal",
		auth.BearerTokens(map[string]string{s.tasks.Config.ServiceToken: "coding-runtime"}),
	)
	s.taskController.RegisterRoutes(router, internal)
	s.projectController.RegisterRoutes(router)
	s.evaluationController.RegisterRoutes(internal)
	s.platformController.RegisterRoutes(router)
	router.GET("/readyz", func(c *core.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if err := s.database.Ping(ctx); err != nil {
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
		group, workerCtx := errgroup.WithContext(ctx)
		group.Go(func() error { return s.tasks.RunDispatcher(workerCtx) })
		group.Go(func() error {
			tick := time.NewTicker(time.Second)
			defer tick.Stop()
			for {
				select {
				case <-workerCtx.Done():
					return nil
				case <-tick.C:
					if err := s.evaluations.Reap(workerCtx); err != nil && workerCtx.Err() == nil {
						logger.FromContext(workerCtx).
							Errorf(workerCtx, "evaluation environment cleanup failed: %v", err)
					}
				}
			}
		})
		s.runErr = group.Wait()
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
