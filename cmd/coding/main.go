package main

import (
	"context"
	"errors"
	"github.com/FlyTOmeLight/forge-coding-api/internal/coding"
	"github.com/FlyTOmeLight/forge-coding-api/internal/config"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if e := run(); e != nil {
		slog.Error("coding stopped", "error", e)
		os.Exit(1)
	}
}
func run() error {
	c, e := config.Load()
	if e != nil {
		return e
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	a, e := coding.New(ctx, c)
	if e != nil {
		return e
	}
	defer a.DB.Close()
	mode := "api"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	switch mode {
	case "migrate":
		return a.Migrate(ctx)
	case "dispatcher":
		return a.RunDispatcher(ctx)
	case "api":
	default:
		return errors.New("mode must be api, dispatcher or migrate")
	}
	s := &http.Server{Addr: c.Listen, Handler: a.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 60 * time.Second}
	done := make(chan error, 1)
	go func() { done <- s.ListenAndServe() }()
	select {
	case e = <-done:
		if errors.Is(e, http.ErrServerClosed) {
			return nil
		}
		return e
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		e = s.Shutdown(shutdown)
		if e != nil {
			_ = s.Close()
		}
		<-done
		return e
	}
}
