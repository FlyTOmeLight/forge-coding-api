package coding

import (
	"context"
	_ "embed"
	"github.com/FlyTOmeLight/forge-coding-api/internal/config"
	"github.com/FlyTOmeLight/forge-coding-api/internal/remote"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

//go:embed schema.sql
var schema string

type App struct {
	DB     *pgxpool.Pool
	Config config.Config
	HTTP   *http.Client
	GitLab *remote.GitLab
}

func New(ctx context.Context, c config.Config) (*App, error) {
	db, e := pgxpool.New(ctx, c.DatabaseURL)
	if e != nil {
		return nil, e
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	h := &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &App{DB: db, Config: c, HTTP: h, GitLab: &remote.GitLab{URL: c.GitLabURL, Token: c.GitLabToken, HTTP: h}}, nil
}
func (a *App) Migrate(ctx context.Context) error {
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(71477232)"); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, schema); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (a *App) Handler() http.Handler {
	m := gin.New()
	m.HandleMethodNotAllowed = true
	m.RedirectTrailingSlash = false
	_ = m.SetTrustedProxies(nil)
	m.Use(gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, _ any) {
		slog.Error("coding request panic", "route", c.FullPath())
		c.AbortWithStatusJSON(500, Object{"error": Object{"code": "INTERNAL_ERROR"}})
	}))
	a.taskRoutes(m)
	route(m, "GET", "/healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if a.DB.Ping(ctx) != nil {
			JSON(w, 503, Object{"status": "unavailable"})
			return
		}
		JSON(w, 200, Object{"status": "ok", "service": "forge-coding-api"})
	})
	route(m, "POST", "/internal/v1/rounds/:run/start", a.service(a.startRound))
	return m
}
func validName(s string) bool { return strings.TrimSpace(s) != "" && len(s) <= 200 }
