package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	serrors "github.com/FlyTOmeLight/server-go/errors"
)

func TestDependencyRequests(t *testing.T) {
	for _, tc := range []struct {
		name, body, code string
		status           int
	}{
		{name: "framework", body: `{"code":"ScopeMismatch","message":"wrong project","requestId":"req-1","statusCode":103403}`, code: "ScopeMismatch", status: 403},
		{name: "tenant", body: `{"error":{"code":"AUTH_REQUIRED","message":"please log in"}}`, code: "AUTH_REQUIRED", status: 401},
		{name: "non-json", body: "gateway unavailable", code: "HTTPError", status: 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/check" ||
					r.Header.Get("Authorization") != "Bearer token" {
					t.Error("unexpected request")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			h := NewHTTP()
			defer h.CloseIdleConnections()
			err := Request(
				context.Background(),
				h,
				"POST",
				server.URL+"/check",
				"token",
				map[string]string{"scope": "coding"},
				nil,
			)
			var detail *serrors.Error
			if !errors.As(err, &detail) || detail.Status != tc.status || detail.Code != tc.code {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.name == "framework" && (detail.RequestID != "req-1" || detail.StatusCode != 103403) {
				t.Fatalf("framework details lost: %+v", detail)
			}
		})
	}
	h := NewHTTP()
	defer h.CloseIdleConnections()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Request(
		ctx,
		h,
		"GET",
		"http://localhost/check",
		"token",
		nil,
		nil,
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("cancel identity lost: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"value":"` + strings.Repeat("x", 32) + `"}`))
	}))
	defer server.Close()
	var result struct{ Value string }
	if err := RequestLimited(context.Background(), h, "GET", server.URL, "token", nil, &result, 8); err == nil {
		t.Fatal("truncated dependency response accepted")
	}
}
