package access

import (
	"context"

	serrors "github.com/FlyTOmeLight/server-go/errors"
	core "github.com/FlyTOmeLight/server-go/pkg"

	"github.com/FlyTOmeLight/forge-coding-api/internal/client/tenant"
	"github.com/FlyTOmeLight/forge-coding-api/internal/httpapi"
)

func (s *Service) Authorized(fn func(*core.Context, User)) core.HandlerFunc {
	return func(c *core.Context) {
		r := c.Request
		cookie, err := r.Cookie("forge_session")
		if err != nil {
			httpapi.Failure(c, serrors.NewError(401, "AUTH_REQUIRED", "请先登录"))
			return
		}
		u, err := s.tenant.Session(
			r.Context(),
			tenant.SessionRequest{
				Session: cookie.Value,
				Method:  r.Method,
				Origin:  r.Header.Get("Origin"),
				CSRF:    r.Header.Get("X-CSRF-Token"),
			},
		)
		if err != nil {
			httpapi.Failure(c, err)
			return
		}
		if tid, pid := r.PathValue("tid"), r.PathValue("pid"); tid != "" && pid != "" {
			level, err := s.Read(r, u)
			if err != nil {
				httpapi.Failure(c, err)
				return
			}
			granted := projectAccess{userID: u.ID, tenantID: tid, projectID: pid, level: level}
			c.Request = r.WithContext(context.WithValue(r.Context(), projectAccessKey{}, granted))
		}
		fn(c, u)
	}
}
