package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

type Error struct {
	Service    string
	Status     int
	Code       string
	RetryAfter time.Duration
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s (HTTP %d)", e.Service, e.Code, e.Status) }
func IsStatus(err error, status int) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == status
}
func Conflict(service, code string) error { return &Error{Service: service, Status: 409, Code: code} }
func request(ctx context.Context, client *http.Client, service, method, url string, body any, headers http.Header, target any) error {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, url, &buf)
	if err != nil {
		return err
	}
	req.Header = headers.Clone()
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return &Error{Service: service, Status: 0, Code: "UNAVAILABLE"}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		delay := time.Duration(0)
		if s, e := strconv.Atoi(resp.Header.Get("Retry-After")); e == nil {
			delay = time.Duration(s) * time.Second
		} else if date, e := http.ParseTime(resp.Header.Get("Retry-After")); e == nil {
			delay = time.Until(date)
		}
		return &Error{Service: service, Status: resp.StatusCode, Code: "HTTP_ERROR", RetryAfter: delay}
	}
	if target == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return err
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(target); err != nil {
		return &Error{Service: service, Status: 502, Code: "INVALID_RESPONSE"}
	}
	return nil
}
