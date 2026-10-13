package coding

import (
	"context"
	"errors"
	"net/http"
	"time"

	api "github.com/agent-infra/sandbox-sdk-go"
	"github.com/agent-infra/sandbox-sdk-go/client"
	"github.com/agent-infra/sandbox-sdk-go/option"

	"github.com/FlyTOmeLight/forge-coding-api/internal/workspace"
)

func value[T any](p *T, fallback T) T {
	if p == nil {
		return fallback
	}
	return *p
}

func pointer[T any](v T) *T { return &v }

func runAIOChecks(ctx context.Context, endpoint workspace.Endpoint, commands []string) ([]Object, error) {
	c := client.NewClient(
		option.WithBaseURL(endpoint.URL),
		option.WithHTTPHeader(http.Header{"Authorization": []string{"Bearer " + endpoint.Token}}),
		option.WithMaxAttempts(1),
	)
	results := make([]Object, 0, len(commands))
	for _, command := range commands {
		r, err := c.Bash.Exec(
			ctx,
			&api.BashExecRequest{
				Command:     command,
				Env:         map[string]*string{"PYTHONDONTWRITEBYTECODE": pointer("1")},
				ExecDir:     pointer("/workspace/repo"),
				AsyncMode:   pointer(true),
				HardTimeout: pointer(float64(120)),
			},
		)
		if err != nil {
			return nil, err
		}
		if r.Data == nil || !value(r.Success, false) {
			return nil, errors.New("AIO check did not start")
		}
		execution := r.Data
		out, stderr := value(execution.Stdout, ""), value(execution.Stderr, "")
		for execution.Status == "running" {
			page, err := c.Bash.Output(
				ctx,
				&api.BashOutputRequest{
					SessionId:    execution.SessionId,
					CommandId:    &execution.CommandId,
					Offset:       execution.Offset,
					StderrOffset: execution.StderrOffset,
					Wait:         pointer(true),
					WaitTimeout:  pointer(float64(1)),
				},
			)
			if err != nil {
				stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				_, _ = c.Bash.Kill(
					stopCtx,
					&api.BashKillRequest{
						SessionId: execution.SessionId,
						Signal:    pointer("SIGKILL"),
					},
				)
				cancel()
				return nil, err
			}
			if page.Data == nil || page.Data.Command == nil || !value(page.Success, false) {
				return nil, errors.New("AIO check status is unknown")
			}
			out += value(page.Data.Stdout, "")
			stderr += value(page.Data.Stderr, "")
			execution.Status = page.Data.Command.Status
			execution.ExitCode = page.Data.Command.ExitCode
			execution.Offset = page.Data.Offset
			execution.StderrOffset = page.Data.StderrOffset
			if len(out) > 12000 {
				out = out[:12000]
			}
			if len(stderr) > 12000 {
				stderr = stderr[:12000]
			}
		}
		if execution.ExitCode == nil {
			return nil, errors.New("AIO check completed without exit code")
		}
		results = append(
			results,
			Object{
				"command":   command,
				"exitCode":  *execution.ExitCode,
				"passed":    *execution.ExitCode == 0,
				"log":       out + stderr,
				"commandID": execution.CommandId,
			},
		)
	}
	return results, nil
}
