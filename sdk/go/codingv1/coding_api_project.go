package codingv1

import (
	"encoding/json"
	"time"
)

type Settings struct {
	ReleaseID      string     `json:"releaseID,omitempty"`
	AgentID        string     `json:"agentID"`
	AgentVersion   int64      `json:"agentVersion"`
	Version        int64      `json:"version"`
	BaseBranch     string     `json:"baseBranch"`
	Model          string     `json:"model"`
	AllowedPaths   []string   `json:"allowedPaths"`
	Checks         []string   `json:"checks"`
	RepairLimit    int        `json:"repairLimit"`
	TimeoutMinutes int        `json:"timeoutMinutes"`
	CheckedAt      *time.Time `json:"checkedAt"`
}

type SaveSettingsRequest Settings

type SettingsPermissions struct {
	ReadCode           bool `json:"readCode"`
	CreateTask         bool `json:"createTask"`
	ConfigureExecution bool `json:"configureExecution"`
}

type SettingsResponse struct {
	Settings    Settings            `json:"settings"`
	Branches    []json.RawMessage   `json:"branches"`
	Runtime     json.RawMessage     `json:"runtime"`
	Agents      []json.RawMessage   `json:"agents"`
	Permissions SettingsPermissions `json:"permissions"`
}

type SettingsCheck struct {
	Settings Settings `json:"settings"`
	BaseHead string   `json:"baseHead"`
	Message  string   `json:"message"`
}

type Connections struct {
	GitLab   json.RawMessage `json:"gitlab"`
	Model    json.RawMessage `json:"model"`
	Identity json.RawMessage `json:"identity"`
}
