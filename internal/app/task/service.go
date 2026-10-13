package task

import (
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FlyTOmeLight/forge-coding-api/internal/app/access"
	"github.com/FlyTOmeLight/forge-coding-api/internal/app/project"
	"github.com/FlyTOmeLight/forge-coding-api/internal/client/agentic"
	"github.com/FlyTOmeLight/forge-coding-api/internal/client/gitlab"
	"github.com/FlyTOmeLight/forge-coding-api/internal/config"
	"github.com/FlyTOmeLight/forge-coding-api/internal/workspace"
)

// Service owns task transitions, execution reconciliation, and delivery decisions.
// Dependencies are borrowed from the application host.
type Service struct {
	Config      config.Config
	DB          *pgxpool.Pool
	HTTP        *http.Client
	Workspaces  workspace.Provider
	GitLab      *gitlab.GitLab
	Projects    *project.Service
	Permissions *access.Service
	Runtime     *agentic.Client
}

func New(
	cfg config.Config,
	db *pgxpool.Pool,
	h *http.Client,
	ws workspace.Provider,
	g *gitlab.GitLab,
	projects *project.Service,
	permissions *access.Service,
) *Service {
	return &Service{
		Runtime:     agentic.New(cfg.RuntimeURL, cfg.RuntimeToken, h),
		Config:      cfg,
		DB:          db,
		HTTP:        h,
		Workspaces:  ws,
		GitLab:      g,
		Projects:    projects,
		Permissions: permissions,
	}
}

func validName(s string) bool { return strings.TrimSpace(s) != "" && len(s) <= 200 }
