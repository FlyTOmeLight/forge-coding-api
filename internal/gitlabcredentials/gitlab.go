package gitlabcredentials

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	sdk "gitlab.com/gitlab-org/api/client-go"
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

func (r *runner) sdkClient() (*sdk.Client, error) {
	return sdk.NewClient(
		"",
		sdk.WithBaseURL(r.config.URL),
		sdk.WithHTTPClient(r.http),
		sdk.WithoutRetries(),
		sdk.WithCustomLogger(nil),
	)
}

// Credential API failures must not echo returned token values into logs.
func credentialError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	var response *sdk.ErrorResponse
	if errors.As(err, &response) && response.Response != nil {
		return fmt.Errorf("GitLab credential API returned HTTP %d", response.Response.StatusCode)
	}
	return errors.New("GitLab credential API unavailable")
}

func tokenValue(t *sdk.PersonalAccessToken) token {
	result := token{ID: t.ID, UserID: t.UserID, Name: t.Name, Scopes: t.Scopes, Revoked: t.Revoked, Value: t.Token}
	if t.ExpiresAt != nil {
		result.ExpiresAt = t.ExpiresAt.String()
	}
	return result
}

func (r *runner) own(ctx context.Context, credential string) (token, error) {
	client, err := r.sdkClient()
	if err != nil {
		return token{}, credentialError(err)
	}
	status, _, err := client.PersonalAccessTokens.GetSinglePersonalAccessToken(
		sdk.WithContext(ctx),
		sdk.WithToken(sdk.PrivateToken, credential),
	)
	if err != nil {
		return token{}, credentialError(err)
	}
	if status.ID < 1 || status.UserID < 1 || status.Revoked {
		return token{}, errors.New("GitLab credential identity is invalid")
	}
	return tokenValue(status), nil
}

func (r *runner) token(ctx context.Context, credential string, id int64) (token, error) {
	client, err := r.sdkClient()
	if err != nil {
		return token{}, credentialError(err)
	}
	result, _, err := client.PersonalAccessTokens.GetSinglePersonalAccessTokenByID(
		id,
		sdk.WithContext(ctx),
		sdk.WithToken(sdk.PrivateToken, credential),
	)
	if err != nil {
		return token{}, credentialError(err)
	}
	return tokenValue(result), nil
}

func (r *runner) revoke(ctx context.Context, credential string, id int64) error {
	client, err := r.sdkClient()
	if err != nil {
		return credentialError(err)
	}
	_, err = client.PersonalAccessTokens.RevokePersonalAccessTokenByID(
		id,
		sdk.WithContext(ctx),
		sdk.WithToken(sdk.PrivateToken, credential),
	)
	return credentialError(err)
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
	client, err := r.sdkClient()
	if err != nil {
		return token{}, credentialError(err)
	}
	options := []sdk.RequestOptionFunc{sdk.WithContext(ctx), sdk.WithToken(sdk.PrivateToken, previous.Value)}
	list := &sdk.ListPersonalAccessTokensOptions{
		UserID:      &previous.UserID,
		Search:      &name,
		Revoked:     sdk.Ptr(false),
		ListOptions: sdk.ListOptions{PerPage: 100, Page: 1},
	}
	for {
		orphan, response, err := client.PersonalAccessTokens.ListPersonalAccessTokens(list, options...)
		if err != nil {
			return token{}, credentialError(err)
		}
		for _, candidate := range orphan {
			if candidate.Name == name && candidate.UserID == previous.UserID &&
				candidate.ID != previous.ID &&
				sameScopes(candidate.Scopes, previous.Scopes) {
				if err := r.revoke(ctx, previous.Value, candidate.ID); err != nil {
					return token{}, err
				}
			}
		}
		if response.NextPage == 0 {
			break
		}
		list.Page = response.NextPage
	}
	expires := sdk.ISOTime(r.now().AddDate(0, 0, r.config.LifetimeDays))
	created, _, err := client.Users.CreatePersonalAccessToken(
		previous.UserID,
		&sdk.CreatePersonalAccessTokenOptions{Name: &name, Scopes: &previous.Scopes, ExpiresAt: &expires},
		options...)
	if err != nil {
		return token{}, credentialError(err)
	}
	next := tokenValue(created)
	if next.Value == "" || next.ID == previous.ID || next.UserID != previous.UserID ||
		!sameScopes(next.Scopes, previous.Scopes) {
		return token{}, errors.New("replacement credential changed identity or scopes")
	}
	check, err := r.own(ctx, next.Value)
	if err != nil {
		return token{}, err
	}
	if check.ID != next.ID || check.UserID != previous.UserID || !sameScopes(check.Scopes, previous.Scopes) {
		return token{}, errors.New("replacement credential verification failed")
	}
	if _, err = time.Parse("2006-01-02", next.ExpiresAt); err != nil {
		return token{}, errors.New("replacement expiry invalid")
	}
	return next, nil
}
