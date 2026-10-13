package aio

import (
	"context"
	"net/http"

	"github.com/FlyTOmeLight/forge-coding-api/internal/client"
	"github.com/FlyTOmeLight/forge-coding-api/internal/workspace"
)

// Ready checks the control endpoint before starting commands.
func Ready(ctx context.Context, h *http.Client, endpoint workspace.Endpoint) error {
	return client.Request(ctx, h, "GET", endpoint.URL+"/v1/sandbox", endpoint.Token, nil, nil)
}
