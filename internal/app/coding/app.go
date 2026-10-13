package coding

import (
	"context"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FlyTOmeLight/forge-coding-api/internal/client/gitlab"
	"github.com/FlyTOmeLight/forge-coding-api/internal/config"
	"github.com/FlyTOmeLight/forge-coding-api/internal/workspace"
)

type Workspace interface {
	Ensure(context.Context, string, string, workspace.Repository) (workspace.Endpoint, error)
	Access(context.Context, string) (workspace.Endpoint, error)
	Fence(context.Context, string, string, int64) error
	Pause(context.Context, string, string) error
	Resume(context.Context, string, string) error
	Published(context.Context, string, string) error
	Stop(context.Context, string) error
	StopAll(context.Context, string, string) error
	CleanupEvaluation(context.Context, string, string) error
	RunJob(context.Context, string, string, string, map[string]string) ([]byte, error)
	Verifier(context.Context, string, string) (workspace.Endpoint, error)
}

type App struct {
	Workspaces Workspace
	DB         *pgxpool.Pool
	Config     config.Config
	HTTP       *http.Client
	GitLab     *gitlab.GitLab
}

// New borrows resources owned by the application host.
func New(c config.Config, db *pgxpool.Pool, h *http.Client, workspaces Workspace, g *gitlab.GitLab) *App {
	return &App{
		DB:         db,
		Config:     c,
		HTTP:       h,
		Workspaces: workspaces,
		GitLab:     g,
	}
}

func validName(s string) bool { return strings.TrimSpace(s) != "" && len(s) <= 200 }
