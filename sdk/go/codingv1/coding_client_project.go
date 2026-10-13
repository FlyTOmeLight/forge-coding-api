package codingv1

import (
	"context"
	"net/url"
)

func escape(s string) string { return url.PathEscape(s) }

func (c *Client) GetSettings(ctx context.Context, s ProjectScope) (*SettingsResponse, error) {
	base, err := s.path()
	if err != nil {
		return nil, err
	}
	return jsonCall[SettingsResponse](c.projectRequest(ctx, s), "GET", base+"/coding-settings", nil)
}

func (c *Client) SaveSettings(ctx context.Context, s ProjectScope, req *SaveSettingsRequest) (*Settings, error) {
	base, err := s.path()
	if err != nil {
		return nil, err
	}
	return jsonCall[Settings](c.projectRequest(ctx, s), "PUT", base+"/coding-settings", req)
}

func (c *Client) CheckSettings(ctx context.Context, s ProjectScope) (*SettingsCheck, error) {
	base, err := s.path()
	if err != nil {
		return nil, err
	}
	return jsonCall[SettingsCheck](c.projectRequest(ctx, s), "POST", base+"/coding-settings/check", nil)
}

func (c *Client) Connections(ctx context.Context, s ProjectScope) (*Connections, error) {
	return jsonCall[Connections](c.projectRequest(ctx, s), "GET", "/api/v1/platform/connections", nil)
}

func (c *Client) ListReleases(ctx context.Context, s ProjectScope) (*ReleaseList, error) {
	base, err := s.path()
	if err != nil {
		return nil, err
	}
	return jsonCall[ReleaseList](c.projectRequest(ctx, s), "GET", base+"/coding-releases", nil)
}

func (c *Client) EnableRelease(
	ctx context.Context,
	s ProjectScope,
	key string,
	req *EnableReleaseRequest,
) (*Release, error) {
	base, err := s.path()
	if err != nil {
		return nil, err
	}
	return jsonCall[Release](mutation(c.projectRequest(ctx, s), key), "POST", base+"/coding-releases", req)
}

func (c *Client) RestoreRelease(
	ctx context.Context,
	s ProjectScope,
	id, key string,
	req *RestoreReleaseRequest,
) (*Release, error) {
	base, err := s.path()
	if err != nil {
		return nil, err
	}
	return jsonCall[Release](
		mutation(c.projectRequest(ctx, s), key),
		"POST",
		base+"/coding-releases/"+escape(id)+"/restore",
		req,
	)
}
