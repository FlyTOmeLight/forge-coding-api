package coding

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	serrors "github.com/FlyTOmeLight/server-go/errors"
	"github.com/gin-gonic/gin"
)

func TestCodingJSONBinding(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{name: "valid", body: `{"summary":"ok"}`, status: 0},
		{name: "unknown-field", body: `{"summary":"ok","extra":true}`, status: 400},
		{name: "two-documents", body: `{"summary":"ok"}{}`, status: 400},
		{name: "oversized", body: `{"summary":"` + strings.Repeat("x", 65536) + `"}`, status: 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/", strings.NewReader(tc.body))
			var req struct {
				Summary string `json:"summary"`
			}
			err := readJSON(c, &req)
			if tc.status == 0 {
				if err != nil || req.Summary != "ok" {
					t.Fatal(err)
				}
				return
			}
			var detail *serrors.Error
			if !errors.As(err, &detail) || detail.Status != tc.status || detail.Code != "INVALID_JSON" {
				t.Fatalf("wrong binding result: %v", err)
			}
		})
	}
}
