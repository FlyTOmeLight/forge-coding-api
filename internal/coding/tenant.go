package coding

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
)

func (a *App) request(ctx context.Context, method, endpoint, token string, body, target any) error {
	var b bytes.Buffer
	if body != nil {
		if e := json.NewEncoder(&b).Encode(body); e != nil {
			return e
		}
	}
	r, e := http.NewRequestWithContext(ctx, method, endpoint, &b)
	if e != nil {
		return e
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	response, e := a.HTTP.Do(r)
	if e != nil {
		return problem(503, "DEPENDENCY_UNAVAILABLE", "依赖服务暂不可用")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var v struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 65536)).Decode(&v)
		if v.Error.Code == "" {
			v.Error.Code = "DEPENDENCY_REJECTED"
			v.Error.Message = "依赖服务拒绝请求"
		}
		return problem(response.StatusCode, v.Error.Code, v.Error.Message)
	}
	if target == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(target)
}
func (a *App) authorized(fn func(http.ResponseWriter, *http.Request, User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, e := r.Cookie("forge_session")
		if e != nil {
			fail(w, problem(401, "AUTH_REQUIRED", "请先登录"))
			return
		}
		var u User
		e = a.request(r.Context(), "POST", a.Config.TenantURL+"/internal/v1/session/check", a.Config.TenantToken, Object{"session": c.Value, "method": r.Method, "origin": r.Header.Get("Origin"), "csrf": r.Header.Get("X-CSRF-Token")}, &u)
		if e != nil {
			fail(w, e)
			return
		}
		fn(w, r, u)
	}
}
func (a *App) service(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.Config.ServiceToken == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+a.Config.ServiceToken)) != 1 {
			fail(w, problem(401, "SERVICE_AUTH_REQUIRED", "服务身份校验失败"))
			return
		}
		fn(w, r)
	}
}
func (a *App) access(ctx context.Context, uid, tid, pid string) (Object, error) {
	var v struct {
		Level int `json:"level"`
	}
	e := a.request(ctx, "POST", a.Config.TenantURL+"/internal/v1/access/check", a.Config.TenantToken, Object{"user_id": uid, "tenant_id": tid, "project_id": pid, "action": "read"}, &v)
	return Object{"level": v.Level}, e
}

type Project struct {
	ID       string `json:"id"`
	TenantID string `json:"tenant_id"`
	Path     string `json:"path"`
	GitLabID int64  `json:"gitlab_id"`
}

func (a *App) project(ctx context.Context, tid, pid string) (Project, error) {
	var p Project
	e := a.request(ctx, "POST", a.Config.TenantURL+"/internal/v1/projects/resolve", a.Config.TenantToken, Object{"tenant_id": tid, "project_id": pid}, &p)
	return p, e
}
