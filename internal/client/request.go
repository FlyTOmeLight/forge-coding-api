package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	serrors "github.com/FlyTOmeLight/server-go/errors"
	"github.com/FlyTOmeLight/server-go/sdk"
)

func Request(ctx context.Context, client *http.Client, method, endpoint, token string, body, target any) error {
	return RequestLimited(ctx, client, method, endpoint, token, body, target, 4<<20)
}

func RequestLimited(
	ctx context.Context,
	client *http.Client,
	method, endpoint, token string,
	body, target any,
	limit int64,
) error {
	var b bytes.Buffer
	if body != nil {
		if e := json.NewEncoder(&b).Encode(body); e != nil {
			return e
		}
	}
	r, e := http.NewRequestWithContext(ctx, method, endpoint, &b)
	if e != nil {
		return e
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	response, e := client.Do(r)
	if e != nil {
		return errors.Join(serrors.NewError(503, "DEPENDENCY_UNAVAILABLE", "依赖服务暂不可用"), e)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var v struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		raw, err := io.ReadAll(io.LimitReader(response.Body, 65536))
		if err != nil {
			return err
		}
		_ = json.Unmarshal(raw, &v)
		if v.Error.Code == "" {
			return sdk.DecodeError(response.StatusCode, bytes.NewReader(raw))
		}
		return serrors.NewError(response.StatusCode, v.Error.Code, v.Error.Message)
	}
	if target == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(response.Body, limit)).Decode(target)
}
