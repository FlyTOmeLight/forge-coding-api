package coding

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/FlyTOmeLight/forge-coding-api/internal/client"
)

type CodingRelease struct {
	ID                 string         `json:"id"`
	ActorID            string         `json:"actorID"`
	ActorName          string         `json:"actorName"`
	Action             string         `json:"action"`
	RestoredFrom       string         `json:"restoredFrom,omitempty"`
	SourceAgentID      string         `json:"sourceAgentID,omitempty"`
	SourceAgentVersion int64          `json:"sourceAgentVersion,omitempty"`
	ExperimentID       string         `json:"experimentID,omitempty"`
	SuiteDigest        string         `json:"suiteDigest,omitempty"`
	Settings           CodingSettings `json:"settings"`
	CreatedAt          time.Time      `json:"createdAt"`
}

const releaseColumns = `id,actor_id,actor_name,action,restored_from,source_agent_id,source_agent_version,experiment_id,suite_digest,settings,created_at`

func scanRelease(row pgx.Row) (CodingRelease, error) {
	var out CodingRelease
	var raw []byte
	err := row.Scan(
		&out.ID,
		&out.ActorID,
		&out.ActorName,
		&out.Action,
		&out.RestoredFrom,
		&out.SourceAgentID,
		&out.SourceAgentVersion,
		&out.ExperimentID,
		&out.SuiteDigest,
		&raw,
		&out.CreatedAt,
	)
	if err == nil {
		err = json.Unmarshal(raw, &out.Settings)
	}
	return out, err
}

func (a *App) getCodingReleases(gc *gin.Context, u User) {
	r := gc.Request
	level, err := a.taskRead(r, u)
	if err != nil {
		fail(gc, err)
		return
	}
	rows, err := a.DB.Query(
		r.Context(),
		`SELECT `+releaseColumns+` FROM coding_project_releases WHERE tenant_id=$1 AND project_id=$2 ORDER BY created_at DESC,id DESC LIMIT 100`,
		r.PathValue("tid"),
		r.PathValue("pid"),
	)
	if err != nil {
		fail(gc, err)
		return
	}
	defer rows.Close()
	out := []CodingRelease{}
	for rows.Next() {
		item, err := scanRelease(rows)
		if err != nil {
			fail(gc, err)
			return
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		fail(gc, err)
		return
	}
	gc.JSON(200, Object{"items": out, "canEnable": level >= 40})
}

func (a *App) enableCodingRelease(gc *gin.Context, u User) {
	a.changeCodingRelease(gc, u, false)
}

func (a *App) restoreCodingRelease(gc *gin.Context, u User) {
	a.changeCodingRelease(gc, u, true)
}

func (a *App) changeCodingRelease(gc *gin.Context, u User, restore bool) {
	r := gc.Request
	level, err := a.taskRead(r, u)
	if err != nil {
		fail(gc, err)
		return
	}
	if level < 40 {
		fail(gc, problem(403, "PROJECT_ADMIN_REQUIRED", "启用或回退需要项目管理员权限"))
		return
	}
	var req struct {
		SettingsVersion int64  `json:"settingsVersion"`
		ExperimentID    string `json:"experimentID"`
		AgentID         string `json:"agentID"`
		AgentVersion    int64  `json:"agentVersion"`
	}
	if err := readJSON(gc, &req); err != nil {
		fail(gc, err)
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" || len(key) > 100 {
		fail(gc, problem(400, "IDEMPOTENCY_REQUIRED", "启用或回退需要唯一请求标识"))
		return
	}
	raw, _ := json.Marshal(req)
	hash := Hash(string(raw) + ":" + u.ID + ":" + r.PathValue("release"))
	tid, pid := r.PathValue("tid"), r.PathValue("pid")
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		fail(gc, err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(
		r.Context(),
		`SELECT pg_advisory_xact_lock(hashtextextended($1,0))`,
		"coding-settings:"+pid,
	); err != nil {
		fail(gc, err)
		return
	}
	var previousHash string
	err = tx.QueryRow(r.Context(), `SELECT request_hash FROM coding_project_releases WHERE tenant_id=$1 AND project_id=$2 AND request_key=$3`, tid, pid, key).
		Scan(&previousHash)
	if err == nil {
		if previousHash != hash {
			fail(gc, problem(409, "IDEMPOTENCY_CONFLICT", "请求标识已用于其他内容"))
			return
		}
		previous, err := scanRelease(
			tx.QueryRow(
				r.Context(),
				`SELECT `+releaseColumns+` FROM coding_project_releases WHERE tenant_id=$1 AND project_id=$2 AND request_key=$3`,
				tid,
				pid,
				key,
			),
		)
		if err != nil {
			fail(gc, err)
			return
		}
		gc.JSON(200, previous)
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		fail(gc, err)
		return
	}
	var current CodingSettings
	var settingsRaw []byte
	var version int64
	var checked *time.Time
	err = tx.QueryRow(r.Context(), `SELECT version,settings,checked_at FROM coding_project_settings WHERE tenant_id=$1 AND project_id=$2 FOR UPDATE`, tid, pid).
		Scan(&version, &settingsRaw, &checked)
	if errors.Is(err, pgx.ErrNoRows) {
		fail(gc, problem(409, "PROJECT_NOT_READY", "请先配置项目基准分支和执行参数"))
		return
	}
	if err != nil {
		fail(gc, err)
		return
	}
	if err = json.Unmarshal(settingsRaw, &current); err != nil {
		fail(gc, err)
		return
	}
	current.Version, current.CheckedAt = version, checked
	if current.Version != req.SettingsVersion {
		fail(gc, problem(412, "STALE_CONFIG", "项目生效配置已变化，请刷新后核对"))
		return
	}
	next := current
	release := CodingRelease{
		ID:                 ID("release"),
		ActorID:            u.ID,
		ActorName:          u.Name,
		Action:             "Enable",
		SourceAgentID:      req.AgentID,
		SourceAgentVersion: req.AgentVersion,
		ExperimentID:       req.ExperimentID,
	}
	if restore {
		target, err := scanRelease(
			tx.QueryRow(
				r.Context(),
				`SELECT `+releaseColumns+` FROM coding_project_releases WHERE tenant_id=$1 AND project_id=$2 AND id=$3`,
				tid,
				pid,
				r.PathValue("release"),
			),
		)
		if errors.Is(err, pgx.ErrNoRows) {
			fail(gc, problem(404, "NOT_FOUND", "此项目中没有该启用记录"))
			return
		}
		if err != nil {
			fail(gc, err)
			return
		}
		next = target.Settings
		release.Action, release.RestoredFrom, release.SourceAgentID, release.SourceAgentVersion, release.ExperimentID, release.SuiteDigest = "Restore", target.ID, target.SourceAgentID, target.SourceAgentVersion, target.ExperimentID, target.SuiteDigest
	} else {
		var approval struct {
			ExperimentID   string `json:"experimentID"`
			SuiteDigest    string `json:"suiteDigest"`
			AgentID        string `json:"agentID"`
			AgentVersion   int64  `json:"agentVersion"`
			RuntimeAgentID string `json:"runtimeAgentID"`
			RuntimeVersion int64  `json:"runtimeVersion"`
			Model          string `json:"model"`
			Scenario       struct {
				BaseHead     string   `json:"baseHead"`
				AllowedPaths []string `json:"allowedPaths"`
				Checks       []string `json:"checks"`
			} `json:"scenario"`
		}
		err := client.Request(
			r.Context(),
			a.HTTP,
			"POST",
			a.Config.RuntimeURL+"/internal/v1/coding-release-approval",
			a.Config.RuntimeToken,
			Object{
				"tenantID":     tid,
				"projectID":    pid,
				"actorID":      u.ID,
				"experimentID": req.ExperimentID,
				"agentID":      req.AgentID,
				"agentVersion": req.AgentVersion,
			},
			&approval,
		)
		if err != nil {
			fail(gc, err)
			return
		}
		if approval.AgentID != req.AgentID || approval.AgentVersion != req.AgentVersion ||
			approval.ExperimentID != req.ExperimentID ||
			approval.SuiteDigest == "" ||
			approval.RuntimeAgentID == "" ||
			approval.RuntimeVersion < 1 {
			fail(gc, problem(502, "INVALID_APPROVAL", "评测审批结果不完整"))
			return
		}
		project, err := a.taskProject(r.Context(), tid, pid)
		if err != nil {
			fail(gc, err)
			return
		}
		branch, err := a.GitLab.Branch(r.Context(), project, current.BaseBranch)
		if err != nil {
			fail(gc, err)
			return
		}
		if branch.Commit.ID != approval.Scenario.BaseHead {
			fail(gc, problem(409, "EVALUATION_BASE_CHANGED", "项目基准分支已变化，请用当前提交重新评测"))
			return
		}
		next.AgentID, next.AgentVersion, next.Model, next.AllowedPaths, next.Checks = approval.RuntimeAgentID, approval.RuntimeVersion, approval.Model, approval.Scenario.AllowedPaths, approval.Scenario.Checks
		release.SuiteDigest = approval.SuiteDigest
	}
	if err := next.validate(); err != nil {
		fail(gc, err)
		return
	}
	if err := a.checkAgentBinding(r.Context(), tid, pid, &next); err != nil {
		fail(gc, err)
		return
	}
	if restore {
		project, err := a.taskProject(r.Context(), tid, pid)
		if err != nil {
			fail(gc, err)
			return
		}
		if _, err = a.GitLab.Branch(r.Context(), project, next.BaseBranch); err != nil {
			fail(gc, err)
			return
		}
	}
	if current.ReleaseID == "" && current.AgentID != "" && current.AgentVersion > 0 {
		initial := CodingRelease{
			ID:        ID("release"),
			ActorID:   u.ID,
			ActorName: u.Name,
			Action:    "Baseline",
			Settings:  current,
		}
		initial.Settings.ReleaseID = initial.ID
		if _, err := insertRelease(r, tx, tid, pid, "baseline:"+initial.ID, "", initial); err != nil {
			fail(gc, err)
			return
		}
	}
	now := time.Now().UTC()
	next.ReleaseID, next.Version, next.CheckedAt = release.ID, current.Version+1, &now
	release.Settings = next
	release, err = insertRelease(r, tx, tid, pid, key, hash, release)
	if err != nil {
		fail(gc, err)
		return
	}
	nextRaw, _ := json.Marshal(next)
	if _, err = tx.Exec(
		r.Context(),
		`UPDATE coding_project_settings SET settings=$3,version=version+1,checked_at=$4 WHERE tenant_id=$1 AND project_id=$2`,
		tid,
		pid,
		nextRaw,
		now,
	); err != nil {
		fail(gc, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		fail(gc, err)
		return
	}
	gc.JSON(201, release)
}

func insertRelease(r *http.Request, tx pgx.Tx, tid, pid, key, hash string, item CodingRelease) (CodingRelease, error) {
	raw, _ := json.Marshal(item.Settings)
	return scanRelease(
		tx.QueryRow(
			r.Context(),
			`INSERT INTO coding_project_releases(id,tenant_id,project_id,request_key,request_hash,actor_id,actor_name,action,restored_from,source_agent_id,source_agent_version,experiment_id,suite_digest,settings) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) RETURNING `+releaseColumns,
			item.ID,
			tid,
			pid,
			key,
			hash,
			item.ActorID,
			item.ActorName,
			item.Action,
			item.RestoredFrom,
			item.SourceAgentID,
			item.SourceAgentVersion,
			item.ExperimentID,
			item.SuiteDigest,
			raw,
		),
	)
}

func sameReleaseConfiguration(a, b CodingSettings) bool {
	return a.AgentID == b.AgentID && a.AgentVersion == b.AgentVersion && a.BaseBranch == b.BaseBranch &&
		reflect.DeepEqual(a.AllowedPaths, b.AllowedPaths) &&
		reflect.DeepEqual(a.Checks, b.Checks)
}
