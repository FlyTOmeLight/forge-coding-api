package platform

import (
	"net/http"

	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"

	"github.com/FlyTOmeLight/forge-coding-api/internal/app/access"
	"github.com/FlyTOmeLight/forge-coding-api/internal/client/gitlab"
	"github.com/FlyTOmeLight/forge-coding-api/internal/client/model"
	"github.com/FlyTOmeLight/forge-coding-api/internal/client/tenant"
	"github.com/FlyTOmeLight/forge-coding-api/internal/config"
)

type Service struct {
	cfg    config.Config
	http   *http.Client
	gitlab *gitlab.GitLab
	tenant *tenant.Client
}

func New(cfg config.Config, h *http.Client, g *gitlab.GitLab) *Service {
	return &Service{cfg: cfg, http: h, gitlab: g, tenant: tenant.New(cfg.TenantURL, cfg.TenantToken, h)}
}

func (s *Service) Connections(c *core.Context, u access.User) (map[string]any, error) {
	if !u.Operator {
		return nil, serrors.NewError(403, "FORBIDDEN", "需要平台管理权限")
	}
	ctx := c.Request.Context()
	models, err := model.Names(ctx, s.http, s.cfg.ModelURL)
	modelStatus := "available"
	if err != nil {
		modelStatus = "unavailable"
		models = []string{}
	}
	_, err = s.gitlab.Version(ctx)
	gitStatus := "available"
	if err != nil {
		gitStatus = "unavailable"
	}
	identityStatus := "available"
	if !s.tenant.Ready(ctx) {
		identityStatus = "unavailable"
	}
	return map[string]any{
		"gitlab":   map[string]any{"status": gitStatus, "url": s.cfg.GitLabPublicURL},
		"model":    map[string]any{"status": modelStatus, "models": models},
		"identity": map[string]any{"status": identityStatus},
	}, nil
}
