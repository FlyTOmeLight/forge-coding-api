package codingv1

import "time"

type Release struct {
	ID                 string    `json:"id"`
	ActorID            string    `json:"actorID"`
	ActorName          string    `json:"actorName"`
	Action             string    `json:"action"`
	RestoredFrom       string    `json:"restoredFrom,omitempty"`
	SourceAgentID      string    `json:"sourceAgentID,omitempty"`
	SourceAgentVersion int64     `json:"sourceAgentVersion,omitempty"`
	ExperimentID       string    `json:"experimentID,omitempty"`
	SuiteDigest        string    `json:"suiteDigest,omitempty"`
	Settings           Settings  `json:"settings"`
	CreatedAt          time.Time `json:"createdAt"`
}

type EnableReleaseRequest struct {
	SettingsVersion int64  `json:"settingsVersion"`
	ExperimentID    string `json:"experimentID"`
	AgentID         string `json:"agentID"`
	AgentVersion    int64  `json:"agentVersion"`
}

type RestoreReleaseRequest struct {
	SettingsVersion int64 `json:"settingsVersion"`
}

type ReleaseList struct {
	Items     []Release `json:"items"`
	CanEnable bool      `json:"canEnable"`
}
