package coding

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/FlyTOmeLight/server-go/conf"
	"github.com/FlyTOmeLight/server-go/host"
	"github.com/FlyTOmeLight/server-go/logger"
	core "github.com/FlyTOmeLight/server-go/pkg"
)

type testService struct{ app *App }

func (s testService) RegisterRoutes(r *core.RouterGroup) error { return s.app.RegisterRoutes(r) }

func (testService) Start(context.Context) error { return nil }

func (testService) Stop(context.Context) error { return nil }

func (a *App) Handler() http.Handler {
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
