package agentic

import (
	"context"
	"net/url"
	"strconv"

	"github.com/FlyTOmeLight/forge-coding-api/internal/client"
)

type AgentChoice struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	PublishedVersion int64  `json:"publishedVersion"`
}

type AgentTool struct {
	Name   string `json:"name"`
	EndRun bool   `json:"endRun"`
	URL    string `json:"url"`
}

type PublishedAgentConfig struct {
	Model struct {
		Name string `json:"name"`
	} `json:"model"`
	Tools    []AgentTool `json:"tools"`
	Toolsets []string    `json:"toolsets"`
}

type PublishedAgent struct {
	AgentID string               `json:"agentID"`
	Number  int64                `json:"number"`
	Config  PublishedAgentConfig `json:"config"`
}

func (c *Client) Ready(ctx context.Context) bool {
	return client.Request(ctx, c.http, "GET", c.endpoint+"/internal/v1/healthz", c.token, nil, nil) == nil
}

func (c *Client) Agent(ctx context.Context, id string) (AgentChoice, error) {
	var out AgentChoice
	err := client.Request(ctx, c.http, "GET", c.endpoint+"/v1/agents/"+url.PathEscape(id), c.token, nil, &out)
	return out, err
}

func (c *Client) AgentVersion(ctx context.Context, id string, version int64) (PublishedAgent, error) {
	var out PublishedAgent
	err := client.Request(
		ctx,
		c.http,
		"GET",
		c.endpoint+"/v1/agents/"+url.PathEscape(id)+"/versions/"+strconv.FormatInt(version, 10),
		c.token,
		nil,
		&out,
	)
	return out, err
}
