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

	"github.com/FlyTOmeLight/server-go/app"
	"github.com/FlyTOmeLight/server-go/conf"
	core "github.com/FlyTOmeLight/server-go/pkg"

	"github.com/FlyTOmeLight/forge-coding-api/internal/gitlabcredentials"
	"github.com/FlyTOmeLight/forge-coding-api/internal/workspace"
	"github.com/FlyTOmeLight/forge-coding-api/service"
)

func main() {
	if err := run(); err != nil {
		slog.Error("coding stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
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
	switch mode {
	case "migrate":
		return service.Migrate(ctx, "")
	case "dispatcher":
		return service.RunDispatcher(ctx, "")
	default:
		return app.Run(ctx, app.Options{
			CloseTimeout: 15 * time.Second,
			Setup: func(engine *core.Engine, _ *conf.Config) error {
				engine.RedirectTrailingSlash = false
				return nil
			},
		}, service.Build)
	}
}
