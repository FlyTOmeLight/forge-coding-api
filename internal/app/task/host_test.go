package task

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/FlyTOmeLight/server-go/auth"
	"github.com/FlyTOmeLight/server-go/conf"
	"github.com/FlyTOmeLight/server-go/host"
	"github.com/FlyTOmeLight/server-go/logger"
	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FlyTOmeLight/forge-coding-api/internal/app/access"
	"github.com/FlyTOmeLight/forge-coding-api/internal/app/evaluation"
	"github.com/FlyTOmeLight/forge-coding-api/internal/app/platform"
	"github.com/FlyTOmeLight/forge-coding-api/internal/app/project"
	"github.com/FlyTOmeLight/forge-coding-api/internal/client/agentic"
	"github.com/FlyTOmeLight/forge-coding-api/internal/client/gitlab"
	"github.com/FlyTOmeLight/forge-coding-api/internal/codingid"
	"github.com/FlyTOmeLight/forge-coding-api/internal/config"
	"github.com/FlyTOmeLight/forge-coding-api/internal/httpapi"
	"github.com/FlyTOmeLight/forge-coding-api/internal/workspace"
	codingv1 "github.com/FlyTOmeLight/forge-coding-api/sdk/go/codingv1"
)

type testService struct{ app *testApplication }

func (s testService) RegisterRoutes(r *core.RouterGroup) error {
	a := s.app
	projects := a.projectService()
	a.Projects = projects
	a.Permissions = projects.Permissions
	a.Runtime = agentic.New(a.Config.RuntimeURL, a.Config.RuntimeToken, a.HTTP)
	r.Use(httpapi.PathValues)
	internal := r.Group("/internal", auth.BearerTokens(map[string]string{a.Config.ServiceToken: "coding-runtime"}))
	NewController(a.Service).RegisterRoutes(r, internal)
	project.NewController(projects).RegisterRoutes(r)
	evaluation.NewController(evaluation.New(a.Config, a.DB, a.HTTP, a.Workspaces, projects, a.Permissions)).
		RegisterRoutes(internal)
	platform.NewController(platform.New(a.Config, a.HTTP, a.GitLab), a.Permissions).RegisterRoutes(r)
	return nil
}

func (testService) Start(context.Context) error { return nil }

func (testService) Stop(context.Context) error { return nil }

func (a *testApplication) Handler() http.Handler {
	manager, err := conf.NewManagerFromConfig(
		&conf.Config{AppName: "forge-coding-api", AppCode: 103, RunMode: conf.RunModeTest},
	)
	if err != nil {
		panic(err)
	}
	log, err := logger.NewLogger(&logger.Config{})
	if err != nil {
		panic(err)
	}
	h, err := host.NewHTTPHost(
		manager,
		log,
		func(e *core.Engine, _ *conf.Config) error { e.RedirectTrailingSlash = false; return nil },
		testService{app: a},
	)
	if err != nil {
		panic(err)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer h.Close()
		defer manager.Close(context.Background())
		defer log.Close()
		h.Handler().ServeHTTP(w, r)
	})
}

func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// testApplication composes real business modules for cross-domain HTTP fixtures.
// Its mutable dependencies belong to each isolated fixture.
type testApplication struct{ *Service }

func newTestApplication(
	cfg config.Config,
	db *pgxpool.Pool,
	h *http.Client,
	ws workspace.Provider,
	g *gitlab.GitLab,
) *testApplication {
	permissions := access.New(cfg, h)
	projects := project.New(cfg, db, h, g, permissions)
	return &testApplication{Service: New(cfg, db, h, ws, g, projects, permissions)}
}

func (a *testApplication) projectService() *project.Service {
	return project.New(a.Config, a.DB, a.HTTP, a.GitLab, access.New(a.Config, a.HTTP))
}

type User = access.User

type Project = access.Project

type CodingSettings = project.Settings

type AgentChoice = agentic.AgentChoice

type PublishedAgent = agentic.PublishedAgent

type PublishedAgentConfig = agentic.PublishedAgentConfig

type AgentTool = agentic.AgentTool

type evaluationEvidence = evaluation.Evidence

type evaluationRequest = codingv1.EvaluationEnvironmentRequest

type evaluationScope = codingv1.EvaluationScope

type evaluationScenario = codingv1.EvaluationScenario

var Token = codingid.Token

type RuntimeRun = agentic.Run

type Object = map[string]any
