package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	server "github.com/FlyTOmeLight/server-go"
	"github.com/FlyTOmeLight/server-go/conf"
	"github.com/FlyTOmeLight/server-go/host"
	"github.com/FlyTOmeLight/server-go/logger"
	"github.com/gin-gonic/gin"

	"github.com/FlyTOmeLight/forge-coding-api/internal/config"
	"github.com/FlyTOmeLight/forge-coding-api/internal/gitlabcredentials"
	"github.com/FlyTOmeLight/forge-coding-api/internal/service"
	"github.com/FlyTOmeLight/forge-coding-api/internal/workspace"
)

func main() {
	if err := run(); err != nil {
		slog.Error("coding stopped", "error", err)
		os.Exit(1)
	}
}

func run() (result error) {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	mode := "api"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	switch {
	case mode == "gitlab-credentials":
		ctx, cancel := context.WithTimeout(ctx, 8*time.Minute)
		defer cancel()
		return gitlabcredentials.Run(ctx)
	case mode == "gitlab-credential-status":
		return credentialStatus()
	case strings.HasPrefix(mode, "workspace-"):
		return workspace.Helper(ctx, strings.TrimPrefix(mode, "workspace-"))
	case mode != "api" && mode != "dispatcher" && mode != "migrate":
		return errors.New("mode must be api, dispatcher or migrate")
	}
	manager, err := conf.NewManager("")
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		result = errors.Join(result, manager.Close(closeCtx))
	}()
	cfg, err := config.FromSnapshot(manager.Snapshot())
	if err != nil {
		return err
	}
	log, err := logger.NewLogger(&logger.Config{Outputs: []logger.Output{{Type: "Console", Level: "Info"}}})
	if err != nil {
		return err
	}
	defer log.Close()
	svc := service.New(cfg, mode == "dispatcher")
	bootstrap := server.NewBootstrap(svc)
	if err := bootstrap.Initialize(ctx); err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		result = errors.Join(result, bootstrap.Close(closeCtx))
	}()
	switch mode {
	case "migrate":
		return svc.Migrate(ctx)
	case "dispatcher":
		return svc.RunDispatcher(ctx)
	}
	h, err := host.NewHTTPHost(manager, log, func(engine *gin.Engine, _ *conf.Config) error {
		engine.RedirectTrailingSlash = false
		return nil
	}, svc)
	if err != nil {
		return err
	}
	defer h.Close()
	return h.Run(ctx)
}
