package gitlab

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOfficialSDKPaginationCancellationAndNoWriteRetry(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch r.URL.Path {
		case "/api/v4/projects/17/repository/branches":
			if r.URL.Query().Get("per_page") != "100" {
				t.Error("missing page size")
			}
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Query().Get("page") == "1" {
				w.Header().Set("X-Next-Page", "2")
				_, _ = w.Write([]byte(`[{"name":"one","commit":{"id":"a"}}]`))
			} else {
				_, _ = w.Write([]byte(`[{"name":"two","commit":{"id":"b"}}]`))
			}
		case "/api/v4/projects/17/merge_requests":
			w.WriteHeader(503)
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	c, err := New(server.URL, "token", "", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	branches, err := c.Branches(context.Background(), 17)
	if err != nil || len(branches) != 2 || branches[1].Name != "two" || requests != 2 {
		t.Fatal(branches, err, requests)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Branch(ctx, 17, "branch"); !errors.Is(err, context.Canceled) {
		t.Fatal("SDK cancellation lost", err)
	}
	requests = 0
	if _, err := c.CreateDraftMergeRequest(
		context.Background(),
		17,
		"source",
		"target",
		"fix",
		"done",
	); !IsStatus(err, 503) ||
		requests != 1 {
		t.Fatal("unsafe retry", err, requests)
	}
	if _, err := c.Branch(context.Background(), 17, "missing"); !IsStatus(err, 404) {
		t.Fatal("SDK not-found mapping lost", err)
	}
}
