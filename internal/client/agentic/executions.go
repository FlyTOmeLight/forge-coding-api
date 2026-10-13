package agentic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/FlyTOmeLight/forge-coding-api/internal/client"
)

type Run struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Output string `json:"output"`
	Error  string `json:"error"`
}

func (c *Client) Lease(ctx context.Context, id string, epoch int64) (bool, error) {
	var result struct {
		Allowed bool `json:"allowed"`
	}
	err := client.Request(
		ctx,
		c.http,
		"GET",
		c.endpoint+"/internal/v1/executions/"+url.PathEscape(id)+"/lease?epoch="+strconv.FormatInt(epoch, 10),
		c.token,
		nil,
		&result,
	)
	return result.Allowed, err
}

func (c *Client) Submit(ctx context.Context, key string, execution []byte) (Run, error) {
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.endpoint+"/internal/v1/executions",
		bytes.NewReader(execution),
	)
	if err != nil {
		return Run{}, err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", key)
	response, err := c.http.Do(request)
	if err != nil {
		return Run{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusAccepted {
		return Run{}, fmt.Errorf("runtime submit HTTP %d", response.StatusCode)
	}
	var run Run
	if err = json.NewDecoder(response.Body).Decode(&run); err != nil {
		return Run{}, err
	}
	if run.ID == "" {
		return Run{}, errors.New("runtime returned empty execution ID")
	}
	return run, nil
}

func (c *Client) Cancel(ctx context.Context, id string) error {
	return client.Request(
		ctx,
		c.http,
		"POST",
		c.endpoint+"/v1/runs/"+url.PathEscape(id)+"/cancel",
		c.token,
		map[string]any{},
		nil,
	)
}

func (c *Client) GetRun(ctx context.Context, id string) (Run, error) {
	var run Run
	err := client.Request(ctx, c.http, "GET", c.endpoint+"/v1/runs/"+url.PathEscape(id), c.token, nil, &run)
	return run, err
}
