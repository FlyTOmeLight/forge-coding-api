package codingv1

type EvaluationScope struct {
	TenantID  string `json:"tenantID"`
	ProjectID string `json:"projectID"`
	ActorID   string `json:"actorID"`
}

type EvaluationScenario struct {
	BaseHead     string   `json:"baseHead"`
	AllowedPaths []string `json:"allowedPaths"`
	Checks       []string `json:"checks"`
}

type EvaluationEnvironmentRequest struct {
	Scope    EvaluationScope    `json:"scope"`
	Scenario EvaluationScenario `json:"scenario"`
	Phase    string             `json:"phase,omitempty"`
}

type EvaluationEvidence struct {
	Status   string        `json:"status"`
	BaseHead string        `json:"baseHead"`
	Patch    string        `json:"patch,omitempty"`
	Changed  []string      `json:"changed,omitempty"`
	Checks   []CheckResult `json:"checks,omitempty"`
	Error    string        `json:"error,omitempty"`
	Stopped  bool          `json:"stopped"`
}
