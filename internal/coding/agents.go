package coding

import (
	"context"
	"net/url"
	"strconv"
	"strings"
)

type AgentChoice struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	PublishedVersion int64  `json:"publishedVersion"`
}
type AgentTool struct {
	Name   string `json:"name"`
	EndRun bool   `json:"endRun"`
	URL    string `json:"url"`
}
type PublishedAgentConfig struct {
	Model struct {
		Name string `json:"name"`
	} `json:"model"`
	Tools    []AgentTool `json:"tools"`
	Toolsets []string    `json:"toolsets"`
}
type PublishedAgent struct {
	AgentID string               `json:"agentID"`
	Number  int64                `json:"number"`
	Config  PublishedAgentConfig `json:"config"`
}

func (a *App) publishedAgents(ctx context.Context) ([]AgentChoice, error) {
	agents := []AgentChoice{}
	after := ""
	for {
		var page struct {
			Items []AgentChoice `json:"items"`
		}
		if e := a.request(ctx, "GET", a.Config.RuntimeURL+"/v1/agents?limit=100&after="+url.QueryEscape(after), a.Config.RuntimeToken, nil, &page); e != nil {
			return nil, e
		}
		for _, agent := range page.Items {
			if agent.PublishedVersion > 0 {
				agents = append(agents, agent)
			}
		}
		if len(page.Items) < 100 {
			return agents, nil
		}
		after = page.Items[len(page.Items)-1].ID
	}
}

// Only publication metadata is returned to the application. Prompt, model
// credentials and tool schemas remain owned by the Agent platform.
func (a *App) checkAgentBinding(ctx context.Context, tid, pid string, c *CodingSettings) error {
	e := a.requireProjectExecutor(ctx, tid, pid)
	if e != nil {
		return e
	}
	var version PublishedAgent
	if e = a.request(ctx, "GET", a.Config.RuntimeURL+"/v1/agents/"+url.PathEscape(c.AgentID)+"/versions/"+strconv.FormatInt(c.AgentVersion, 10), a.Config.RuntimeToken, nil, &version); e != nil {
		return problem(409, "AGENT_VERSION_UNAVAILABLE", "无法读取所选 Agent 发布版本，请检查平台配置")
	}
	if version.AgentID != c.AgentID || version.Number != c.AgentVersion {
		return problem(409, "AGENT_VERSION_UNAVAILABLE", "Agent 发布版本不匹配")
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
				finish = tool.EndRun && tool.URL == strings.TrimRight(a.Config.SandboxGatewayURL, "/")+"/internal/v1/tools/finish"
			}
			if tool.Name == "run_checks" {
				checks = tool.URL == strings.TrimRight(a.Config.SandboxGatewayURL, "/")+"/internal/v1/tools/run_checks"
			}
		}
		if !filesystem || !finish || !checks {
			return problem(409, "AGENT_WORKSPACE_MISMATCH", "Agent must use the bound AIO toolset and platform result tools")
		}
		c.Model = version.Config.Model.Name
		return nil
	}
}
