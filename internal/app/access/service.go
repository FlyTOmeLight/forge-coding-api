// Package access enforces the shared session and project visibility rules.
package access

import (
	"context"
	"net/http"

	serrors "github.com/FlyTOmeLight/server-go/errors"

	"github.com/FlyTOmeLight/forge-coding-api/internal/client/tenant"
	"github.com/FlyTOmeLight/forge-coding-api/internal/config"
)

type User = tenant.User

type Project = tenant.Project

type Service struct{ tenant *tenant.Client }

type projectAccessKey struct{}

type projectAccess struct {
	userID, tenantID, projectID string
	level                       int
}

func New(cfg config.Config, h *http.Client) *Service {
	return &Service{tenant: tenant.New(cfg.TenantURL, cfg.TenantToken, h)}
}

func (s *Service) Level(ctx context.Context, uid, tid, pid string) (int, error) {
	return s.tenant.Level(ctx, uid, tid, pid)
}

func (s *Service) Resolve(ctx context.Context, tid, pid string) (Project, error) {
	return s.tenant.Resolve(ctx, tid, pid)
}

func (s *Service) Read(r *http.Request, u User) (int, error) {
	tid, pid := r.PathValue("tid"), r.PathValue("pid")
	if granted, ok := r.Context().Value(projectAccessKey{}).(projectAccess); ok && granted.userID == u.ID &&
		granted.tenantID == tid &&
		granted.projectID == pid {
		return granted.level, nil
	}
	level, err := s.Level(r.Context(), u.ID, tid, pid)
	if err != nil {
		return 0, err
	}
	if level < 20 {
		return 0, serrors.NewError(404, "NOT_FOUND", "任务不存在或无代码访问权限")
	}
	return level, nil
}
