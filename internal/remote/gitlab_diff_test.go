package remote

import (
	"context"
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
		{"raw patch", "diff --git a/src/calc.py b/src/calc.py\n+    # 中文注释\n", 200, ""},
		{"upstream rejection", "not authorized", 403, "HTTP_ERROR"},
		{"oversized patch", strings.Repeat("x", (4<<20)+1), 200, "RESPONSE_TOO_LARGE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/api/v4/projects/17/merge_requests/9/raw_diffs" || r.Header.Get("PRIVATE-TOKEN") != "fixture-token" {
					t.Error("wrong diff request")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			g := GitLab{URL: server.URL, Token: "fixture-token", HTTP: server.Client()}
			patch, err := g.RawMergeRequestDiff(context.Background(), 17, 9)
			if tc.want == "" {
				if err != nil || patch != tc.body {
					t.Fatal(patch, err)
				}
				return
			}
			failure, ok := err.(*Error)
			if !ok || failure.Code != tc.want || patch != "" {
				t.Fatal(patch, err)
			}
		})
	}
}
