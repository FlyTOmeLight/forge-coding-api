package gitlab

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRawMergeRequestDiffPreservesTextAndRejectsIncompleteResults(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       string
	}{
		{name: "raw patch", body: "diff --git a/src/calc.py b/src/calc.py\n+    # 中文注释\n", status: 200, want: ""},
		{name: "upstream rejection", body: "not authorized", status: 403, want: "HTTP_ERROR"},
		{name: "oversized patch", body: strings.Repeat("x", (4<<20)+1), status: 200, want: "RESPONSE_TOO_LARGE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" ||
					r.URL.Path != "/api/v4/projects/17/merge_requests/9/raw_diffs" ||
					r.Header.Get("PRIVATE-TOKEN") != "fixture-token" {
					t.Error("wrong diff request")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			g, err := New(server.URL, "fixture-token", "", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			patch, err := g.RawMergeRequestDiff(context.Background(), 17, 9)
			if tc.want == "" {
				if err != nil || patch != tc.body {
					t.Fatal("patch bytes", len(patch), err)
				}
				return
			}
			if patch != "" || (tc.status == 403 && !IsStatus(err, 403)) ||
				(tc.status == 200 && !errors.Is(err, ErrDiffTooLarge)) {
				t.Fatal("patch bytes", len(patch), err)
			}
		})
	}
}
