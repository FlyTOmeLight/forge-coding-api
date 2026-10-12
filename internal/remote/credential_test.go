package remote

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestGitLabLoadsProjectedCredentialWithoutRestart(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	current := filepath.Join(root, "token")
	os.WriteFile(first, []byte("first-token"), 0600)
	os.WriteFile(second, []byte("second-token"), 0600)
	os.Symlink(first, current)
	got := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("PRIVATE-TOKEN"))
		w.Write([]byte(`{}`))
	}))
	defer server.Close()
	g := GitLab{URL: server.URL, Token: "stale-startup-token", TokenFile: current, HTTP: server.Client()}
	var target map[string]any
	if err := g.call(context.Background(), "GET", "/user", nil, &target); err != nil {
		t.Fatal(err)
	}
	next := filepath.Join(root, "next")
	os.Symlink(second, next)
	os.Rename(next, current)
	if err := g.call(context.Background(), "GET", "/user", nil, &target); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "first-token" || got[1] != "second-token" {
		t.Fatal(got)
	}
	os.Remove(current)
	if err := g.call(context.Background(), "GET", "/user", nil, &target); err == nil {
		t.Fatal("missing Secret fell back to stale credential")
	}
	if len(got) != 2 {
		t.Fatal("request sent with invalid credential")
	}
}
