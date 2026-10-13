package access

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	core "github.com/FlyTOmeLight/server-go/pkg"
	"github.com/gin-gonic/gin"

	"github.com/FlyTOmeLight/forge-coding-api/internal/config"
	"github.com/FlyTOmeLight/forge-coding-api/internal/httpapi"
)

func TestProjectAuthorizationPrecedesBinding(t *testing.T) {
	for _, tc := range []struct {
		name, body    string
		level, status int
	}{
		{name: "hidden-project", body: `{"unknown":true}`, level: 0, status: 404},
		{name: "visible-project", body: `{}`, level: 30, status: 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checks := 0
			tenant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "session/check") {
					json.NewEncoder(w).Encode(User{ID: "user"})
					return
				}
				checks++
				json.NewEncoder(w).Encode(map[string]int{"level": tc.level})
			}))
			defer tenant.Close()
			permissions := New(config.Config{TenantURL: tenant.URL}, tenant.Client())
			engine := gin.New()
			engine.Use(httpapi.PathValues)
			engine.POST(
				"/tenants/:tid/projects/:pid",
				permissions.Authorized(func(c *core.Context, u User) {
					if _, err := permissions.Read(c.Request, u); err != nil {
						httpapi.Failure(c, err)
						return
					}
					var req struct{}
					if err := httpapi.ReadJSON(c, &req); err != nil {
						httpapi.Failure(c, err)
						return
					}
					c.Status(200)
				}),
			)
			req := httptest.NewRequest("POST", "/tenants/t/projects/p", strings.NewReader(tc.body))
			req.AddCookie(&http.Cookie{Name: "forge_session", Value: "session"})
			out := httptest.NewRecorder()
			engine.ServeHTTP(out, req)
			if out.Code != tc.status {
				t.Fatalf("status %d: %s", out.Code, out.Body.String())
			}
			if checks != 1 {
				t.Fatalf("project permission fetched %d times", checks)
			}
		})
	}
}
