package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	server "github.com/FlyTOmeLight/server-go"
	"github.com/FlyTOmeLight/server-go/conf"
	"github.com/FlyTOmeLight/server-go/host"
	"github.com/FlyTOmeLight/server-go/logger"

	"github.com/FlyTOmeLight/forge-coding-api/internal/app/coding"
	"github.com/FlyTOmeLight/forge-coding-api/internal/config"
	"github.com/FlyTOmeLight/forge-coding-api/internal/table"
)

func TestFrameworkResourceLifecycle(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin, err := table.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := "test_" + coding.Token()[:16]
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	endpoint, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := endpoint.Query()
	query.Set("search_path", schema)
	endpoint.RawQuery = query.Encode()
	r := &resources{databaseURL: endpoint.String()}
	bootstrap := server.NewBootstrap(r)
	if err := bootstrap.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	defer bootstrap.Close(context.Background())
	svc, err := New(config.Config{ServiceToken: strings.Repeat("s", 32)}, r.db, r.http, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := table.Migrate(ctx, r.db); err != nil {
		t.Fatal(err)
	}
	manager, err := conf.NewManagerFromConfig(
		&conf.Config{
			AppName:    "forge-coding-api",
			AppCode:    103,
			RunMode:    conf.RunModeTest,
			HTTPServer: conf.HTTPServerConfig{Listen: "127.0.0.1:0"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	log, err := logger.NewLogger(&logger.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	h, err := host.NewHTTPHost(manager, log, nil, svc)
	if err != nil {
		t.Fatal(err)
	}
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- h.Run(runCtx) }()
	// Poll only until the host has bound its ephemeral listener.
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for h.Address() == "" {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case err := <-done:
			t.Fatal(err)
		case <-tick.C:
		}
	}
	httpClient := &http.Client{Timeout: 2 * time.Second}
	for _, tc := range []struct {
		path   string
		status int
	}{
		{path: "/readyz", status: 200}, {path: "/healthz", status: 200}, {path: "/missing", status: 404}, {path: "/api/v1/tenants/t/projects/p/tasks", status: 401},
	} {
		req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+h.Address()+tc.path, nil)
		resp, err := httpClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != tc.status {
			t.Fatalf("%s: %d %s %v", tc.path, resp.StatusCode, body, err)
		}
		if tc.status == 404 {
			var result struct {
				RequestID string `json:"requestId"`
			}
			if err := json.Unmarshal(body, &result); err != nil || result.RequestID == "" {
				t.Fatal("framework request ID missing", string(body), err)
			}
		}

		if tc.status == 401 {
			var result struct{ Error struct{ Code string } }
			if err := json.Unmarshal(body, &result); err != nil || result.Error.Code != "AUTH_REQUIRED" {
				t.Fatal("business envelope changed", string(body), err)
			}
		}
	}
	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// The separately deployed dispatcher cancels and joins all of its workers.
	worker, err := New(config.Config{}, r.db, r.http, true)
	if err != nil {
		t.Fatal(err)
	}
	workerCtx, stopWorker := context.WithCancel(ctx)
	if err := worker.Start(workerCtx); err != nil {
		t.Fatal(err)
	}
	stopWorker()
	if err := worker.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.db.Ping(ctx); err != nil {
		t.Fatalf("borrowed pool closed by service: %v", err)
	}
	if err := bootstrap.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := svc.app.DB.Ping(ctx); err == nil {
		t.Fatal("pool retained after close")
	}
	canceled, stopCanceled := context.WithCancel(ctx)
	stopCanceled()
	if err := server.NewBootstrap(&resources{databaseURL: endpoint.String()}).
		Initialize(canceled); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("startup cancellation lost: %v", err)
	}
}
