package agentic

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"

	"github.com/FlyTOmeLight/forge-coding-api/internal/client"
	codingv1 "github.com/FlyTOmeLight/forge-coding-api/sdk/go/codingv1"
)

func (c *Client) Trace(ctx context.Context, id string, after int64) (codingv1.RuntimeTrace, error) {
	trace := codingv1.RuntimeTrace{Events: []json.RawMessage{}, NextSeq: after}
	err := client.RequestLimited(
		ctx,
		c.http,
		"GET",
		c.endpoint+"/v1/runs/"+url.PathEscape(id)+"/trace?after="+strconv.FormatInt(after, 10),
		c.token,
		nil,
		&trace,
		32<<20,
	)
	return trace, err
}
