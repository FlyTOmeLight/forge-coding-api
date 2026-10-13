package codingv1

import (
	"encoding/json"
	"io"
	"net/http"
)

type ToolScope struct {
	TenantID  string `json:"tenantID"`
	ProjectID string `json:"projectID"`
	ActorID   string `json:"actorID"`
	TaskID    string `json:"taskID"`
	RoundID   string `json:"roundID"`
}

type ExecutionIdentity struct {
	Scope ToolScope
	RunID string
	Epoch int64
}

type Judgment struct {
	Status  string `json:"status"`
	Summary string `json:"summary"`
}

type FinishRequest struct {
	Summary  string   `json:"summary"`
	Judgment Judgment `json:"judgment"`
}

type FinishResponse struct {
	Accepted bool   `json:"accepted"`
	RoundID  string `json:"roundID"`
	TaskID   string `json:"taskID"`
}

type EvaluationFinishRequest struct {
	Summary  string          `json:"summary"`
	Judgment json.RawMessage `json:"judgment"`
}

type EvaluationFinishResponse struct {
	Accepted     bool   `json:"accepted"`
	Summary      string `json:"summary"`
	Verification string `json:"verification"`
}

type CheckResult struct {
	Command   string `json:"command"`
	ExitCode  int    `json:"exitCode"`
	Log       string `json:"log"`
	Passed    bool   `json:"passed"`
	CommandID string `json:"commandID"`
}

type ChecksResponse struct {
	Checks []CheckResult `json:"checks"`
}

type PermitResponse struct {
	Allowed bool `json:"allowed"`
}

// SandboxResponse streams the upstream body. The caller must close Body.
type SandboxResponse struct {
	Body   io.ReadCloser
	Status int
	Header http.Header
}

type SandboxRequest struct {
	Identity    ExecutionIdentity
	RoundID     string
	TrialID     string
	Method      string
	Path        string
	Body        io.Reader
	ContentType string
}
