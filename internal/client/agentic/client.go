package agentic

import (
	"net/http"
	"strings"
)

// Client translates the Agentic execution protocol and borrows its HTTP transport.
type Client struct {
	endpoint string
	token    string
	http     *http.Client
}

func New(endpoint, token string, h *http.Client) *Client {
	return &Client{endpoint: strings.TrimRight(endpoint, "/"), token: token, http: h}
}
