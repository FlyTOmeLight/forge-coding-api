package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/FlyTOmeLight/forge-coding-api/internal/client/gitlab"
)

// Invoked only through the platform's authenticated Kubernetes exec. It emits
// credential metadata, never token values, using the same client as the service.
func credentialStatus() error {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	h := &http.Client{Transport: transport, Timeout: 15 * time.Second}
	defer h.CloseIdleConnections()
	g, err := gitlab.New(os.Getenv("GITLAB_URL"), os.Getenv("GITLAB_TOKEN"), os.Getenv("GITLAB_TOKEN_FILE"), h)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	status, err := g.CredentialStatus(ctx)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(status)
}
