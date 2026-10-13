package agentic

import (
	"context"
	"net/url"
	"strconv"

	"github.com/FlyTOmeLight/forge-coding-api/internal/client"
)

func (c *Client) EvaluationLease(ctx context.Context, trialID, runID string, epoch int64) (bool, error) {
	var out struct {
		Allowed bool `json:"allowed"`
	}
	err := client.Request(
		ctx,
		c.http,
		"GET",
		c.endpoint+"/internal/v1/evaluation-trials/"+trialID+"/lease?runID="+url.QueryEscape(
			runID,
		)+"&epoch="+strconv.FormatInt(
			epoch,
			10,
		),
		c.token,
		nil,
		&out,
	)
	return out.Allowed, err
}
