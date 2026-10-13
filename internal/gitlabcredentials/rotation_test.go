package gitlabcredentials

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	kt "k8s.io/client-go/testing"
)

type fixture struct {
	r       *runner
	kube    *fake.Clientset
	tokens  map[string]*token
	issued  int
	deleted []int64
	clock   time.Time
}

func setup(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{clock: time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC), tokens: map[string]*token{}}
	f.tokens["old-secret"] = &token{
		ID:        7,
		UserID:    1,
		Name:      "service",
		Scopes:    []string{"api"},
		ExpiresAt: f.clock.AddDate(0, 0, 5).Format("2006-01-02"),
		Value:     "old-secret",
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		auth := f.tokens[req.Header.Get("PRIVATE-TOKEN")]
		if auth == nil || auth.Revoked {
			w.WriteHeader(401)
			w.Write([]byte(`{"error":"old-secret should never appear in errors"}`))
			return
		}
		path := req.URL.Path
		if path == "/api/v4/personal_access_tokens/self" {
			json.NewEncoder(w).Encode(auth)
			return
		}
		if path == "/api/v4/personal_access_tokens" {
			result := []token{}
			for _, item := range f.tokens {
				if !item.Revoked && item.ID != 7 {
					copy := *item
					copy.Value = ""
					result = append(result, copy)
				}
			}
			json.NewEncoder(w).Encode(result)
			return
		}
		if path == "/api/v4/users/1/personal_access_tokens" && req.Method == "POST" {
			var body struct {
				Name      string
				Scopes    []string
				ExpiresAt string `json:"expires_at"`
			}
			json.NewDecoder(req.Body).Decode(&body)
			if !sameScopes(body.Scopes, []string{"api"}) {
				t.Error("scope changed", body.Scopes)
			}
			f.issued++
			value := fmt.Sprintf("new-secret-%d", f.issued)
			next := &token{
				ID:        int64(7 + f.issued),
				UserID:    1,
				Name:      body.Name,
				Scopes:    body.Scopes,
				ExpiresAt: body.ExpiresAt,
				Value:     value,
			}
			f.tokens[value] = next
			json.NewEncoder(w).Encode(next)
			return
		}
		var id int64
		fmt.Sscanf(path, "/api/v4/personal_access_tokens/%d", &id)
		for _, item := range f.tokens {
			if item.ID == id {
				if req.Method == "DELETE" {
					item.Revoked = true
					f.deleted = append(f.deleted, id)
					w.WriteHeader(204)
					return
				}
				copy := *item
				copy.Value = ""
				json.NewEncoder(w).Encode(copy)
				return
			}
		}
		w.WriteHeader(404)
	}))
	t.Cleanup(server.Close)
	f.kube = fake.NewClientset(
		&core.Secret{
			ObjectMeta: meta.ObjectMeta{Name: "source", Namespace: "primary"},
			Data: map[string][]byte{
				"GITLAB_TOKEN": []byte("old-secret"),
				"untouched":    []byte("other-setting"),
			},
		},
		&core.Secret{ObjectMeta: meta.ObjectMeta{Name: "state", Namespace: "primary"}},
		&core.Secret{
			ObjectMeta: meta.ObjectMeta{Name: "consumer", Namespace: "other"},
			Data:       map[string][]byte{"GITLAB_TOKEN": []byte("old-secret")},
		},
		&core.Secret{
			ObjectMeta: meta.ObjectMeta{Name: "aio-task", Namespace: "primary"},
			Data: map[string][]byte{
				"git-token": []byte("old-secret"),
				"aio-token": []byte("sandbox-key"),
			},
		},
		&core.Pod{
			ObjectMeta: meta.ObjectMeta{
				Name:      "api",
				Namespace: "primary",
				Labels:    map[string]string{"app": "api"},
			},
			Status: core.PodStatus{
				Phase:             core.PodRunning,
				ContainerStatuses: []core.ContainerStatus{{Name: "api", Ready: true}},
			},
		},
		&core.Pod{
			ObjectMeta: meta.ObjectMeta{
				Name:      "worker",
				Namespace: "other",
				Labels:    map[string]string{"app": "worker"},
			},
			Status: core.PodStatus{
				Phase:             core.PodRunning,
				ContainerStatuses: []core.ContainerStatus{{Name: "worker", Ready: true}},
			},
		},
	)
	f.r = &runner{
		config: Config{
			URL:         server.URL,
			Source:      Reference{Namespace: "primary", Name: "source", Key: "GITLAB_TOKEN"},
			StateSecret: "state",
			Namespaces:  []string{"primary", "other"},
			Targets: []Probe{
				{Namespace: "primary", Deployment: "api", Container: "api"},
				{Namespace: "other", Deployment: "worker", Container: "worker"},
			},
			BeforeDays:   14,
			LifetimeDays: 90,
		},
		kube: f.kube,
		http: server.Client(),
		now:  func() time.Time { return f.clock },
	}
	f.r.execute = func(ctx context.Context, p Probe, _ *core.Pod) (token, error) {
		name := "source"
		if p.Namespace == "other" {
			name = "consumer"
		}
		s, err := f.kube.CoreV1().Secrets(p.Namespace).Get(ctx, name, meta.GetOptions{})
		if err != nil {
			return token{}, err
		}
		return f.r.own(ctx, string(s.Data["GITLAB_TOKEN"]))
	}
	return f
}

func TestRotationRetainsOldUntilAllConsumersLoad(t *testing.T) {
	f := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	execute := f.r.execute
	f.r.execute = func(context.Context, Probe, *core.Pod) (token, error) {
		return token{}, errors.New("projection delayed")
	}
	if err := f.r.maintain(ctx); err == nil {
		t.Fatal("rotation completed before hot load")
	}
	cancel()
	if f.tokens["old-secret"].Revoked || f.issued != 1 {
		t.Fatal("old credential revoked prematurely")
	}
	f.r.execute = execute
	if err := f.r.maintain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.issued != 1 || !f.tokens["old-secret"].Revoked {
		t.Fatal("resume reissued credential or did not retire old")
	}
	source, _ := f.kube.CoreV1().Secrets("primary").Get(context.Background(), "source", meta.GetOptions{})
	if string(source.Data["untouched"]) != "other-setting" {
		t.Fatal("unrelated config overwritten")
	}
	task, _ := f.kube.CoreV1().Secrets("primary").Get(context.Background(), "aio-task", meta.GetOptions{})
	if string(task.Data["git-token"]) != "new-secret-1" || string(task.Data["aio-token"]) != "sandbox-key" {
		t.Fatal("task credential not updated")
	}
	_, saved, err := f.r.load(context.Background())
	if err != nil || saved.Next.Value != "" || saved.Previous.Value != "" {
		t.Fatal("retired plaintext retained", err)
	}
}

func TestPartialDistributionResumesSavedCredential(t *testing.T) {
	f := setup(t)
	blocked := true
	f.kube.PrependReactor("update", "secrets", func(action kt.Action) (bool, runtime.Object, error) {
		s := action.(kt.UpdateAction).GetObject().(*core.Secret)
		if s.Name == "consumer" && blocked {
			return true, nil, errors.New("secret update interrupted")
		}
		return false, nil, nil
	})
	if err := f.r.maintain(context.Background()); err == nil {
		t.Fatal("partial update ignored")
	}
	if f.tokens["old-secret"].Revoked {
		t.Fatal("partial switch invalidated old consumer")
	}
	blocked = false
	if err := f.r.maintain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.issued != 1 {
		t.Fatal("saved replacement was reissued")
	}
}

func TestLostIssuanceSaveReconcilesOrphan(t *testing.T) {
	f := setup(t)
	once := true
	f.kube.PrependReactor("update", "secrets", func(action kt.Action) (bool, runtime.Object, error) {
		s := action.(kt.UpdateAction).GetObject().(*core.Secret)
		if s.Name == "state" && once {
			once = false
			return true, nil, errors.New("save interrupted")
		}
		return false, nil, nil
	})
	if err := f.r.maintain(context.Background()); err == nil {
		t.Fatal("failed persistence ignored")
	}
	if f.tokens["old-secret"].Revoked {
		t.Fatal("source invalidated before persistence")
	}
	if err := f.r.maintain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.issued != 2 || !f.tokens["new-secret-1"].Revoked || f.tokens["new-secret-2"].Revoked {
		t.Fatal("orphan or replacement mishandled")
	}
}

func TestOverlapAndActiveGitOperationDelayRetirement(t *testing.T) {
	f := setup(t)
	f.r.config.GraceSeconds = 86400
	if err := f.r.maintain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.tokens["old-secret"].Revoked {
		t.Fatal("overlap not retained")
	}
	f.clock = f.clock.Add(25 * time.Hour)
	pod := &core.Pod{
		ObjectMeta: meta.ObjectMeta{
			Name:      "publish",
			Namespace: "primary",
			Labels:    map[string]string{"app": "coding-artifact"},
		},
		Status: core.PodStatus{Phase: core.PodRunning},
	}
	f.kube.CoreV1().Pods("primary").Create(context.Background(), pod, meta.CreateOptions{})
	if err := f.r.maintain(context.Background()); err == nil {
		t.Fatal("active Git operation was ignored")
	}
	if f.tokens["old-secret"].Revoked {
		t.Fatal("active Git operation lost auth")
	}
	pod.Status.Phase = core.PodSucceeded
	f.kube.CoreV1().Pods("primary").Update(context.Background(), pod, meta.UpdateOptions{})
	if err := f.r.maintain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !f.tokens["old-secret"].Revoked || f.issued != 1 {
		t.Fatal("retirement failed")
	}
}

func TestFarExpirySkipsIssuanceAndErrorsRedactCredentials(t *testing.T) {
	f := setup(t)
	f.tokens["old-secret"].ExpiresAt = f.clock.AddDate(0, 0, 90).Format("2006-01-02")
	if err := f.r.maintain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.issued != 0 {
		t.Fatal("far-future credential rotated")
	}
	f.tokens["old-secret"].Revoked = true
	err := f.r.maintain(context.Background())
	if err == nil || strings.Contains(err.Error(), "old-secret") {
		t.Fatal("missing failure or credential disclosure", err)
	}
}

func TestPendingRepositoryPreparationRetainsOldCredential(t *testing.T) {
	f := setup(t)
	pod := &core.Pod{
		ObjectMeta: meta.ObjectMeta{
			Name:      "aio-pending",
			Namespace: "primary",
			Labels:    map[string]string{"app": "coding-aio"},
		},
		Status: core.PodStatus{Phase: core.PodPending},
	}
	f.kube.CoreV1().Pods("primary").Create(context.Background(), pod, meta.CreateOptions{})
	if err := f.r.maintain(context.Background()); err == nil || f.tokens["old-secret"].Revoked {
		t.Fatal("pending preparation lost its credential", err)
	}
	pod.Status.Phase = core.PodRunning
	pod.Status.InitContainerStatuses = []core.ContainerStatus{
		{Name: "prepare", State: core.ContainerState{Terminated: &core.ContainerStateTerminated{ExitCode: 0}}},
	}
	f.kube.CoreV1().Pods("primary").Update(context.Background(), pod, meta.UpdateOptions{})
	if err := f.r.maintain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !f.tokens["old-secret"].Revoked || f.issued != 1 {
		t.Fatal("completed preparation blocked retirement or reissued token")
	}
}

func TestTaskCreatedDuringProjectionGetsReplacement(t *testing.T) {
	f := setup(t)
	execute := f.r.execute
	once := true
	f.r.execute = func(ctx context.Context, p Probe, pod *core.Pod) (token, error) {
		if once {
			once = false
			_, err := f.kube.CoreV1().
				Secrets("primary").
				Create(ctx, &core.Secret{ObjectMeta: meta.ObjectMeta{Name: "late-task", Namespace: "primary"}, Data: map[string][]byte{"git-token": []byte("old-secret")}}, meta.CreateOptions{})
			if err != nil {
				return token{}, err
			}
		}
		return execute(ctx, p, pod)
	}
	if err := f.r.maintain(context.Background()); err != nil {
		t.Fatal(err)
	}
	task, _ := f.kube.CoreV1().Secrets("primary").Get(context.Background(), "late-task", meta.GetOptions{})
	if string(task.Data["git-token"]) != "new-secret-1" {
		t.Fatal("task created during hot loading retained old credential")
	}
}
