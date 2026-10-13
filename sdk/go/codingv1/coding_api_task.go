package codingv1

import (
	"encoding/json"
	"time"
)

// Task keeps code ownership separate from task responsibility and review.
type Task struct {
	AgentJudgment   json.RawMessage `json:"agentJudgment"`
	BaseBranch      string          `json:"baseBranch"`
	Reproduce       string          `json:"reproduce"`
	Constraints     string          `json:"constraints"`
	Reference       string          `json:"reference"`
	ID              string          `json:"id"`
	TenantID        string          `json:"tenantId"`
	ProjectID       string          `json:"projectId"`
	OwnerID         string          `json:"ownerId"`
	Title           string          `json:"title"`
	Description     string          `json:"description"`
	Criteria        string          `json:"criteria"`
	Branch          string          `json:"branch"`
	TargetBranch    string          `json:"targetBranch"`
	MRIID           int64           `json:"mrIid"`
	Head            string          `json:"head"`
	Status          string          `json:"status"`
	Control         string          `json:"control"`
	Version         int64           `json:"version"`
	Epoch           int64           `json:"epoch"`
	Round           int             `json:"round"`
	Checks          string          `json:"checks"`
	CheckedHead     string          `json:"checkedHead"`
	ApprovalHead    string          `json:"approvalHead"`
	ReviewerIDs     []string        `json:"reviewerIds"`
	ActiveRun       *string         `json:"activeRun"`
	StopRequestedAt *time.Time      `json:"stopRequestedAt"`
	StopConfirmed   bool            `json:"stopConfirmed"`
	Discussions     json.RawMessage `json:"discussions"`
	Synthetic       bool            `json:"synthetic"`
	UpdatedAt       time.Time       `json:"updatedAt"`
}

type AssociateTaskRequest struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Criteria    string `json:"criteria"`
	MRIID       int64  `json:"mrIid"`
	Synthetic   bool   `json:"synthetic"`
}

type CreateTaskRequest struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Reproduce   string   `json:"reproduce"`
	Criteria    string   `json:"criteria"`
	Constraints string   `json:"constraints"`
	Reference   string   `json:"reference"`
	BaseBranch  string   `json:"baseBranch"`
	ReviewerIDs []string `json:"reviewerIds"`
	Synthetic   bool     `json:"synthetic"`
}

type TaskActionRequest struct {
	Action     string `json:"action"`
	Version    int64  `json:"version"`
	Head       string `json:"head"`
	Text       string `json:"text"`
	OwnerID    string `json:"ownerId"`
	FeedbackID string `json:"feedbackId"`
}

type TaskList struct {
	Items []Task `json:"items"`
}

type TaskPermissions struct {
	Write  bool `json:"write"`
	Manage bool `json:"manage"`
	Review bool `json:"review"`
}

type TaskDetail struct {
	Task         Task              `json:"task"`
	Permissions  TaskPermissions   `json:"permissions"`
	GitLabURL    string            `json:"gitlabUrl"`
	GitLabTarget string            `json:"gitlabTarget"`
	Feedback     []json.RawMessage `json:"feedback"`
	Events       []json.RawMessage `json:"events"`
	Runs         []json.RawMessage `json:"runs"`
}
