package gitlabcredentials

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

type token struct {
	ID        int64    `json:"id"`
	UserID    int64    `json:"user_id"`
	Name      string   `json:"name"`
	Scopes    []string `json:"scopes"`
	ExpiresAt string   `json:"expires_at"`
	Revoked   bool     `json:"revoked"`
	Value     string   `json:"token,omitempty"`
}

func (r *runner) api(ctx context.Context, credential, method, path string, body, target any) error {
	var data bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&data).Encode(body); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, r.config.URL+"/api/v4"+path, &data)
	if err != nil {
		return err
	}
	req.Header.Set("PRIVATE-TOKEN", credential)
	req.Header.Set("Content-Type", "application/json")
	response, err := r.http.Do(req)
	if err != nil {
		return fmt.Errorf("GitLab credential API request failed: %s", method)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("GitLab credential API %s returned HTTP %d", method, response.StatusCode)
	}
	if target == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return err
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(target); err != nil {
		return fmt.Errorf("invalid GitLab credential API response")
	}
	return nil
}
func (r *runner) own(ctx context.Context, credential string) (token, error) {
	var status token
	err := r.api(ctx, credential, "GET", "/personal_access_tokens/self", nil, &status)
	if err == nil && (status.ID < 1 || status.UserID < 1 || status.Revoked) {
		err = fmt.Errorf("GitLab credential identity is invalid")
	}
	return status, err
}
func sameScopes(a, b []string) bool {
	a = append([]string{}, a...)
	b = append([]string{}, b...)
	sort.Strings(a)
	sort.Strings(b)
	return strings.Join(a, "\x00") == strings.Join(b, "\x00")
}
func (r *runner) create(ctx context.Context, previous token) (token, error) {
	name := fmt.Sprintf("forge-credential-%d", previous.ID)
	// If the process died between issuance and Secret persistence, plaintext is
	// unrecoverable. Only revoke matching orphan candidates; old service auth lives.
	var orphan []token
	query := "/personal_access_tokens?user_id=" + fmt.Sprint(previous.UserID) + "&search=" + url.QueryEscape(name) + "&revoked=false&per_page=100"
	if err := r.api(ctx, previous.Value, "GET", query, nil, &orphan); err != nil {
		return token{}, err
	}
	for _, candidate := range orphan {
		if candidate.Name == name && candidate.UserID == previous.UserID && candidate.ID != previous.ID && sameScopes(candidate.Scopes, previous.Scopes) {
			if err := r.api(ctx, previous.Value, "DELETE", fmt.Sprintf("/personal_access_tokens/%d", candidate.ID), nil, nil); err != nil {
				return token{}, err
			}
		}
	}
	var next token
	err := r.api(ctx, previous.Value, "POST", fmt.Sprintf("/users/%d/personal_access_tokens", previous.UserID), map[string]any{"name": name, "scopes": previous.Scopes, "expires_at": r.now().AddDate(0, 0, r.config.LifetimeDays).Format("2006-01-02")}, &next)
	if err != nil {
		return token{}, err
	}
	if next.Value == "" || next.ID == previous.ID || next.UserID != previous.UserID || !sameScopes(next.Scopes, previous.Scopes) {
		return token{}, fmt.Errorf("replacement credential changed identity or scopes")
	}
	check, err := r.own(ctx, next.Value)
	if err != nil {
		return token{}, err
	}
	if check.ID != next.ID || check.UserID != previous.UserID || !sameScopes(check.Scopes, previous.Scopes) {
		return token{}, fmt.Errorf("replacement credential verification failed")
	}
	if _, err = time.Parse("2006-01-02", next.ExpiresAt); err != nil {
		return token{}, fmt.Errorf("replacement expiry invalid")
	}
	return next, nil
}
