package codingv1

type TaskDiff struct {
	MRIID        int64  `json:"mrIid"`
	Base         string `json:"base"`
	Head         string `json:"head"`
	Start        string `json:"start"`
	SourceBranch string `json:"sourceBranch"`
	TargetBranch string `json:"targetBranch"`
	Patch        string `json:"patch"`
}
