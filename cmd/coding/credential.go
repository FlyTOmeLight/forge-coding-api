package main

import (
	"context"
	"encoding/json"
	"github.com/FlyTOmeLight/forge-coding-api/internal/remote"
	"net/http"
	"os"
	"time"
)

// Invoked only through the platform's authenticated Kubernetes exec. It emits
// credential metadata, never token values, using the same client as the service.
func credentialStatus() error {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	g := remote.GitLab{URL: os.Getenv("GITLAB_URL"), Token: os.Getenv("GITLAB_TOKEN"), TokenFile: os.Getenv("GITLAB_TOKEN_FILE"), HTTP: &http.Client{Transport: transport, Timeout: 15 * time.Second}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	status, err := g.CredentialStatus(ctx)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(status)
}
