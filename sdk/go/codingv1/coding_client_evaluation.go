package codingv1

import "context"

func (c *Client) AdvanceEvaluation(
	ctx context.Context,
	trial string,
	req *EvaluationEnvironmentRequest,
) (*EvaluationEvidence, error) {
	return jsonCall[EvaluationEvidence](
		evaluationRequest(c.http.R().SetContext(ctx)),
		"POST",
		"/internal/v1/evaluation-environments/"+escape(trial),
		req,
	)
}
