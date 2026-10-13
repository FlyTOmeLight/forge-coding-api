package model

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	serrors "github.com/FlyTOmeLight/server-go/errors"
)

func Names(ctx context.Context, h *http.Client, endpoint string) ([]string, error) {
	req, e := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(endpoint, "/")+"/api/tags", nil)
	if e != nil {
		return nil, e
	}
	response, e := h.Do(req)
	if e != nil {
		return nil, e
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, serrors.NewError(502, "MODEL_UNAVAILABLE", "内网模型服务不可用")
	}
	var v struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if e = json.NewDecoder(response.Body).Decode(&v); e != nil {
		return nil, e
	}
	names := []string{}
	for _, m := range v.Models {
		names = append(names, m.Name)
	}
	return names, nil
}
