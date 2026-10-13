package workspace

import "context"

// Provider supplies task and evaluation environment operations.
type Provider interface {
	Ensure(context.Context, string, string, Repository) (Endpoint, error)
	Access(context.Context, string) (Endpoint, error)
	Fence(context.Context, string, string, int64) error
	Pause(context.Context, string, string) error
	Resume(context.Context, string, string) error
	Published(context.Context, string, string) error
	Stop(context.Context, string) error
	StopAll(context.Context, string, string) error
	CleanupEvaluation(context.Context, string, string) error
	RunJob(context.Context, string, string, string, map[string]string) ([]byte, error)
	Verifier(context.Context, string, string) (Endpoint, error)
}
