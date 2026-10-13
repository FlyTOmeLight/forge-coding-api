package codingv1

import "encoding/json"

type RuntimeRun struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Output string `json:"output"`
	Error  string `json:"error"`
}

type RuntimeTrace struct {
	Run *struct {
		RuntimeRun
		Scope *struct {
			TenantID  string `json:"tenantID"`
			ProjectID string `json:"projectID"`
			TaskID    string `json:"taskID"`
			RoundID   string `json:"roundID"`
		} `json:"scope"`
		WaitingStep string `json:"waitingStep,omitempty"`
		EventSeq    int64  `json:"eventSeq"`
		Deadline    string `json:"deadline"`
		Attempt     int    `json:"attempt"`
	} `json:"run"`
	Events      []json.RawMessage `json:"events"`
	LatestEvent json.RawMessage   `json:"latestEvent,omitempty"`
	NextSeq     int64             `json:"nextSeq"`
	HasMore     bool              `json:"hasMore"`
}
