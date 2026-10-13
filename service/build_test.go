package service

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/app"

	"github.com/FlyTOmeLight/forge-coding-api/internal/app/coding"
	"github.com/FlyTOmeLight/forge-coding-api/internal/table"
)

func runTestDatabase(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin, err := table.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := "test_" + coding.Token()[:16]
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	endpoint, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := endpoint.Query()
	query.Set("search_path", schema)
	endpoint.RawQuery = query.Encode()
	return endpoint.String()
}

func runTestOptions(t *testing.T, address, dsn string) app.Options {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	config := fmt.Sprintf(
		"AppName = 'coding-test'\nAppCode = 103\nRunMode = 'test'\n[HTTPServer]\nListen = %q\n[Coding]\nDatabaseURL = %q\nServiceToken = 'test-service-token-with-32-characters'\n",
		address,
		dsn,
	)
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return app.Options{ConfigPath: path, InitTimeout: 10 * time.Second, CloseTimeout: 5 * time.Second}
}

func TestRunClosesConnectionsOnAssemblyFailure(t *testing.T) {
	options := runTestOptions(t, "127.0.0.1:0", runTestDatabase(t))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	r := &resources{}
	err := app.Run(ctx, options, func(initCtx context.Context, runtime *app.Runtime, cfg *Config) error {
		r.databaseURL = cfg.Coding.DatabaseURL
		if err := runtime.Initialize(initCtx, r); err != nil {
			return err
		}
		cfg.Coding.GitLabURL = "://invalid"
		_, err := New(cfg.Coding, r.db, r.http, false)
		return err
	})
	if err == nil {
		t.Fatal("invalid GitLab endpoint accepted during assembly")
	}
	if r.db == nil {
		t.Fatalf("assembly failed before acquiring the database: %v", err)
	}
	if err := r.db.Ping(ctx); err == nil {
		t.Fatal("database retained after assembly failure")
	}
}

func TestRunHTTPResourceOwnership(t *testing.T) {
	for _, owned := range []bool{true, false} {
		t.Run(fmt.Sprintf("runtime-owns-connections=%t", owned), func(t *testing.T) {
			dsn := runTestDatabase(t)
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			address := listener.Addr().String()
			if err := listener.Close(); err != nil {
				t.Fatal(err)
			}
			options := runTestOptions(t, address, dsn)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			runCtx, stop := context.WithCancel(ctx)
			defer stop()
			r := &resources{}
			defer r.Close(context.Background())
			var initialization context.Context
			var runErr error
			assembled := make(chan struct{})
			done := make(chan struct{})
			go func() {
				defer close(done)
				runErr = app.Run(
					runCtx,
					options,
					func(initCtx context.Context, runtime *app.Runtime, cfg *Config) error {
						defer close(assembled)
						initialization = initCtx
						r.databaseURL = cfg.Coding.DatabaseURL
						var err error
						if owned {
							err = runtime.Initialize(initCtx, r)
						} else {
							err = r.Initialize(initCtx)
						}
						if err != nil {
							return err
						}
						if err := table.Migrate(initCtx, r.db); err != nil {
							return err
						}
						svc, err := New(cfg.Coding, r.db, r.http, true)
						if err != nil {
							return err
						}
						return runtime.AddService(svc)
					},
				)
			}()
			defer func() {
				stop()
				select {
				case <-done:
				case <-ctx.Done():
					t.Error("application failed to shut down", ctx.Err())
				}
			}()
			client := &http.Client{Timeout: time.Second}
			defer client.CloseIdleConnections()
			tick := time.NewTicker(10 * time.Millisecond)
			defer tick.Stop()
			for {
				request, err := http.NewRequestWithContext(
					ctx,
					http.MethodGet,
					"http://"+address+"/readyz",
					nil,
				)
				if err != nil {
					t.Fatal(err)
				}
				response, err := client.Do(request)
				if err == nil {
					_, _ = io.Copy(io.Discard, response.Body)
					response.Body.Close()
					if response.StatusCode == http.StatusOK {
						break
					}
				}
				select {
				case <-done:
					t.Fatalf("application exited before readiness: %v", runErr)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-tick.C:
				}
			}
			<-assembled
			if initialization.Err() != context.Canceled {
				t.Fatalf("initialization context retained after startup: %v", initialization.Err())
			}
			stop()
			select {
			case <-done:
				if runErr != nil {
					t.Fatal(runErr)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if err := r.db.Ping(ctx); owned && err == nil {
				t.Fatal("runtime retained its database after shutdown")
			} else if !owned && err != nil {
				t.Fatalf("service closed the caller's shared database: %v", err)
			}
		})
	}
}
