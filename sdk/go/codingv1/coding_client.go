// Package codingv1 contains Forge Coding HTTP contracts and a server-go SDK client.
package codingv1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	framesdk "github.com/FlyTOmeLight/server-go/sdk"
	"resty.dev/v3"
)

type Client struct{ http *framesdk.Client }

// NewClient owns a framework transport with no automatic retries. An optional
// HTTP client supplies TLS, cookies and timeout; its connection pool is borrowed
// when it cannot be cloned. Close after all calls and streams have finished.
func NewClient(endpoint, serviceToken string, h *http.Client) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		(u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("invalid Coding endpoint")
	}
	if strings.ContainsAny(serviceToken, "\r\n") {
		return nil, errors.New("invalid service token")
	}
	cfg := &framesdk.Config{Endpoint: strings.TrimRight(endpoint, "/"), Timeout: 20}
	if serviceToken != "" {
		cfg.Credentials = &framesdk.Credentials{AuthType: framesdk.AuthTypeAPIKey, APIKey: serviceToken}
	}
	transport, err := framesdk.NewClientWithConfig(cfg)
	if err != nil {
		return nil, err
	}
	transport.Client.Client().CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if h != nil {
		if h.Transport != nil {
			rt := h.Transport
			if pool, ok := rt.(*http.Transport); ok {
				rt = pool.Clone()
			}
			transport.SetTransport(rt)
		}
		transport.Client.Client().Jar = h.Jar
		if h.Timeout > 0 {
			transport.SetTimeout(h.Timeout)
		}
	}
	return &Client{http: transport}, nil
}

func (c *Client) Close() error { c.http.Client.Client().CloseIdleConnections(); return c.http.Close() }

// ProjectScope supplies route identity and per-call browser credentials. Session
// and CSRF are never copied into shared client headers or a JSON request body.
type ProjectScope struct{ TenantID, ProjectID, Session, CSRFToken string }

func (s ProjectScope) path() (string, error) {
	if s.TenantID == "" || s.ProjectID == "" {
		return "", errors.New("tenant and project IDs are required")
	}
	return "/api/v1/tenants/" + url.PathEscape(s.TenantID) + "/projects/" + url.PathEscape(s.ProjectID), nil
}

func (c *Client) projectRequest(ctx context.Context, s ProjectScope) *resty.Request {
	r := c.http.R().SetContext(ctx)
	if s.Session != "" {
		r.SetCookies([]*http.Cookie{{Name: "forge_session", Value: s.Session}})
	}
	if s.CSRFToken != "" {
		r.SetHeader("X-CSRF-Token", s.CSRFToken)
	}
	return r
}

func readBody(body io.Reader, limit int64) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("Coding response exceeds size limit")
	}
	return raw, nil
}

func responseError(status int, raw []byte) error {
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &envelope) == nil && len(envelope.Error) > 0 {
		return framesdk.DecodeError(status, bytes.NewReader(envelope.Error))
	}
	return framesdk.DecodeError(status, bytes.NewReader(raw))
}

func jsonCall[T any](r *resty.Request, method, path string, body any) (*T, error) {
	return jsonCallLimit[T](r, method, path, body, 4<<20)
}

func jsonCallLimit[T any](r *resty.Request, method, path string, body any, limit int64) (*T, error) {
	r.SetResponseDoNotParse(true)
	if body != nil {
		r.SetBody(body)
	}
	response, err := r.Execute(method, path)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, err := readBody(response.Body, limit)
	if err != nil {
		return nil, err
	}
	status := response.StatusCode()
	if status < 200 || status >= 300 {
		return nil, responseError(status, raw)
	}
	var value T
	if err = json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("decode Coding response: %w", err)
	}
	return &value, nil
}

func mutation(r *resty.Request, key string) *resty.Request {
	return r.SetHeader("Idempotency-Key", key)
}

func taskPath(s ProjectScope, task string) (string, error) {
	base, err := s.path()
	if err != nil {
		return "", err
	}
	if task == "" {
		return "", errors.New("task ID is required")
	}
	return base + "/tasks/" + url.PathEscape(task), nil
}

// Evaluation reconciliation has a bounded server budget of 180 seconds.
func evaluationRequest(r *resty.Request) *resty.Request { return r.SetTimeout(200 * time.Second) }
