package codingv1

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"resty.dev/v3"
)

func (c *Client) executionRequest(ctx context.Context, i ExecutionIdentity) (*resty.Request, error) {
	if i.RunID == "" || i.Epoch < 1 {
		return nil, errors.New("execution run ID and positive epoch are required")
	}
	scope, err := json.Marshal(i.Scope)
	if err != nil {
		return nil, err
	}
	return c.http.R().
			SetContext(ctx).
			SetHeader("X-Agentic-Scope", string(scope)).
			SetHeader("X-Agentic-Run-ID", i.RunID).
			SetHeader("X-Agentic-Epoch", strconv.FormatInt(i.Epoch, 10)),
		nil
}

func (c *Client) SubmitFinish(
	ctx context.Context,
	i ExecutionIdentity,
	key string,
	req *FinishRequest,
) (*FinishResponse, error) {
	r, err := c.executionRequest(ctx, i)
	if err != nil {
		return nil, err
	}
	return jsonCall[FinishResponse](mutation(r, key), "POST", "/internal/v1/tools/finish", req)
}

func (c *Client) RunChecks(ctx context.Context, i ExecutionIdentity) (*ChecksResponse, error) {
	r, err := c.executionRequest(ctx, i)
	if err != nil {
		return nil, err
	}
	return jsonCall[ChecksResponse](r.SetTimeout(130*time.Second), "POST", "/internal/v1/tools/run_checks", nil)
}

func (c *Client) EvaluationFinish(
	ctx context.Context,
	i ExecutionIdentity,
	req *EvaluationFinishRequest,
) (*EvaluationFinishResponse, error) {
	r, err := c.executionRequest(ctx, i)
	if err != nil {
		return nil, err
	}
	return jsonCall[EvaluationFinishResponse](r, "POST", "/internal/v1/evaluation-tools/finish", req)
}

func (c *Client) EvaluationChecks(ctx context.Context, i ExecutionIdentity) (*ChecksResponse, error) {
	r, err := c.executionRequest(ctx, i)
	if err != nil {
		return nil, err
	}
	return jsonCall[ChecksResponse](
		r.SetTimeout(130*time.Second),
		"POST",
		"/internal/v1/evaluation-tools/run_checks",
		nil,
	)
}

func (c *Client) PublicationPermit(ctx context.Context, round string) (*PermitResponse, error) {
	return jsonCall[PermitResponse](
		c.http.R().SetContext(ctx),
		"POST",
		"/internal/v1/rounds/"+escape(round)+"/publication-permit",
		nil,
	)
}

func (c *Client) Sandbox(ctx context.Context, req *SandboxRequest) (*SandboxResponse, error) {
	if req == nil || ((req.RoundID == "") == (req.TrialID == "")) ||
		(req.Method != "GET" && req.Method != "POST") ||
		(req.Path != "/v1/sandbox" && !strings.HasPrefix(req.Path, "/v1/file/") && !strings.HasPrefix(req.Path, "/v1/bash/")) {
		return nil, errors.New("invalid sandbox operation")
	}
	base := "/internal/v1/rounds/" + escape(req.RoundID) + "/sandbox"
	if req.TrialID != "" {
		base = "/internal/v1/evaluation-environments/" + escape(req.TrialID) + "/sandbox"
	}
	r, err := c.executionRequest(ctx, req.Identity)
	if err != nil {
		return nil, err
	}
	r.SetResponseDoNotParse(true).SetBody(req.Body)
	if req.ContentType != "" {
		r.SetHeader("Content-Type", req.ContentType)
	}
	response, err := r.Execute(req.Method, base+req.Path)
	if err != nil {
		return nil, err
	}
	if response.StatusCode() < 200 || response.StatusCode() >= 300 {
		defer response.Body.Close()
		raw, err := readBody(response.Body, 64<<10)
		if err != nil {
			return nil, err
		}
		return nil, responseError(response.StatusCode(), raw)
	}
	return &SandboxResponse{
		Body:   response.Body,
		Status: response.StatusCode(),
		Header: response.Header().Clone(),
	}, nil
}
