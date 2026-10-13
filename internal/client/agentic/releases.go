package agentic

import (
	"context"

	"github.com/FlyTOmeLight/forge-coding-api/internal/client"
)

type ReleaseApprovalRequest struct {
	TenantID     string `json:"tenantID"`
	ProjectID    string `json:"projectID"`
	ActorID      string `json:"actorID"`
	ExperimentID string `json:"experimentID"`
	AgentID      string `json:"agentID"`
	AgentVersion int64  `json:"agentVersion"`
}

type ReleaseApproval struct {
	ExperimentID   string `json:"experimentID"`
	SuiteDigest    string `json:"suiteDigest"`
	AgentID        string `json:"agentID"`
	AgentVersion   int64  `json:"agentVersion"`
	RuntimeAgentID string `json:"runtimeAgentID"`
	RuntimeVersion int64  `json:"runtimeVersion"`
	Model          string `json:"model"`
	Scenario       struct {
		BaseHead     string   `json:"baseHead"`
		AllowedPaths []string `json:"allowedPaths"`
		Checks       []string `json:"checks"`
	} `json:"scenario"`
}

func (c *Client) ReleaseApproval(ctx context.Context, req ReleaseApprovalRequest) (ReleaseApproval, error) {
	var out ReleaseApproval
	err := client.Request(
		ctx,
		c.http,
		"POST",
		c.endpoint+"/internal/v1/coding-release-approval",
		c.token,
		req,
		&out,
	)
	return out, err
}
