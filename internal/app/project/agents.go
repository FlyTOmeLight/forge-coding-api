package project

import (
	"context"
	"strings"

	serrors "github.com/FlyTOmeLight/server-go/errors"
)

// Only publication metadata is returned to the application. Prompt, model
// credentials and tool schemas remain owned by the Agent platform.
func (a *Service) CheckAgentBinding(ctx context.Context, tid, pid string, c *Settings) error {
	e := a.RequireExecutor(ctx, tid, pid)
	if e != nil {
		return e
	}
	version, e := a.Runtime.AgentVersion(ctx, c.AgentID, c.AgentVersion)
	if e != nil {
		return serrors.NewError(409, "AGENT_VERSION_UNAVAILABLE", "无法读取所选 Agent 发布版本，请检查平台配置")
	}
	if version.AgentID != c.AgentID || version.Number != c.AgentVersion {
		return serrors.NewError(409, "AGENT_VERSION_UNAVAILABLE", "Agent 发布版本不匹配")
	}
	{
		filesystem := false
		for _, set := range version.Config.Toolsets {
			if set == "filesystem" {
				filesystem = true
			}
		}
		finish, checks := false, false
		for _, tool := range version.Config.Tools {
			if tool.Name == "finish" {
				finish = tool.EndRun &&
					tool.URL == strings.TrimRight(
						a.Config.SandboxGatewayURL,
						"/",
					)+"/internal/v1/tools/finish"
			}
			if tool.Name == "run_checks" {
				checks = tool.URL == strings.TrimRight(
					a.Config.SandboxGatewayURL,
					"/",
				)+"/internal/v1/tools/run_checks"
			}
		}
		if !filesystem || !finish || !checks {
			return serrors.NewError(
				409,
				"AGENT_WORKSPACE_MISMATCH",
				"Agent must use the bound AIO toolset and platform result tools",
			)
		}
		c.Model = version.Config.Model.Name
		return nil
	}
}
