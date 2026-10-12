// Package gitlabcredentials owns overlap-based GitLab service credential renewal.
package gitlabcredentials

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

type Reference struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Key       string `json:"key"`
}
type Probe struct {
	Namespace  string `json:"namespace"`
	Deployment string `json:"deployment"`
	Container  string `json:"container"`
}
type Config struct {
	Source       Reference `json:"source"`
	StateSecret  string    `json:"stateSecret"`
	URL          string    `json:"url"`
	Namespaces   []string  `json:"namespaces"`
	Targets      []Probe   `json:"targets"`
	BeforeDays   int       `json:"beforeDays"`
	LifetimeDays int       `json:"lifetimeDays"`
	GraceSeconds int       `json:"graceSeconds"`
}
type state struct {
	Previous    token     `json:"previous"`
	Next        token     `json:"next"`
	Cutover     time.Time `json:"cutover"`
	RetireAfter time.Time `json:"retireAfter"`
}
type runner struct {
	config  Config
	kube    kubernetes.Interface
	rest    *rest.Config
	http    *http.Client
	now     func() time.Time
	execute func(context.Context, Probe, *core.Pod) (token, error)
}

func Run(ctx context.Context) error {
	var cfg Config
	if err := json.Unmarshal([]byte(os.Getenv("GITLAB_ROTATION_CONFIG")), &cfg); err != nil {
		return fmt.Errorf("GITLAB_ROTATION_CONFIG is invalid")
	}
	if cfg.Source.Namespace == "" || cfg.Source.Name == "" || cfg.Source.Key == "" || cfg.StateSecret == "" || cfg.URL == "" || len(cfg.Namespaces) == 0 || len(cfg.Targets) == 0 || cfg.BeforeDays < 1 || cfg.BeforeDays > cfg.LifetimeDays || cfg.LifetimeDays > 365 || cfg.GraceSeconds < 0 {
		return fmt.Errorf("GitLab rotation configuration is incomplete")
	}
	config, err := rest.InClusterConfig()
	if err != nil {
		return err
	}
	config.Timeout = 20 * time.Second
	kube, err := kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	r := runner{config: cfg, kube: kube, rest: config, http: &http.Client{Transport: transport, Timeout: 25 * time.Second}, now: time.Now}
	r.execute = r.probe
	// Native leader election also serializes manually-triggered Jobs with CronJobs.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	completed := make(chan error, 1)
	elector, err := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{
		Lock:          &resourcelock.LeaseLock{LeaseMeta: meta.ObjectMeta{Namespace: cfg.Source.Namespace, Name: cfg.StateSecret}, Client: kube.CoordinationV1(), LockConfig: resourcelock.ResourceLockConfig{Identity: os.Getenv("HOSTNAME")}},
		LeaseDuration: 45 * time.Second, RenewDeadline: 30 * time.Second, RetryPeriod: 5 * time.Second, ReleaseOnCancel: true,
		Callbacks: leaderelection.LeaderCallbacks{OnStartedLeading: func(leading context.Context) { completed <- r.maintain(leading); cancel() }, OnStoppedLeading: func() {}},
	})
	if err != nil {
		return err
	}
	elector.Run(runCtx)
	select {
	case err = <-completed:
		return err
	default:
		return fmt.Errorf("credential rotation leadership ended before completion")
	}
}
func (r *runner) load(ctx context.Context) (*core.Secret, state, error) {
	secret, err := r.kube.CoreV1().Secrets(r.config.Source.Namespace).Get(ctx, r.config.StateSecret, meta.GetOptions{})
	if err != nil {
		return nil, state{}, err
	}
	var saved state
	if raw := secret.Data["rotation.json"]; len(raw) > 0 {
		if err = json.Unmarshal(raw, &saved); err != nil {
			return nil, saved, fmt.Errorf("saved credential rotation state is invalid")
		}
	}
	return secret, saved, nil
}
func (r *runner) save(ctx context.Context, secret *core.Secret, saved state) error {
	raw, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	if secret.Data == nil {
		secret.Data = map[string][]byte{}
	}
	secret.Data["rotation.json"] = raw
	updated, err := r.kube.CoreV1().Secrets(secret.Namespace).Update(ctx, secret, meta.UpdateOptions{})
	if err != nil {
		return err
	}
	*secret = *updated
	return nil
}
func (r *runner) maintain(ctx context.Context) error {
	secret, saved, err := r.load(ctx)
	if err != nil {
		return err
	}
	source, err := r.kube.CoreV1().Secrets(r.config.Source.Namespace).Get(ctx, r.config.Source.Name, meta.GetOptions{})
	if err != nil {
		return err
	}
	credential := string(source.Data[r.config.Source.Key])
	if credential == "" {
		return fmt.Errorf("source credential is missing")
	}
	if saved.Next.Value == "" {
		previous, err := r.own(ctx, credential)
		if err != nil {
			return err
		}
		previous.Value = credential
		expiry, err := time.Parse("2006-01-02", previous.ExpiresAt)
		if err != nil {
			return fmt.Errorf("source credential expiry is invalid")
		}
		if expiry.Sub(r.now()) > time.Duration(r.config.BeforeDays)*24*time.Hour {
			slog.Info("GitLab credential valid", "token_id", previous.ID, "expires_at", previous.ExpiresAt)
			return nil
		}
		next, err := r.create(ctx, previous)
		if err != nil {
			return err
		}
		saved = state{Previous: previous, Next: next}
		// Persist replacement before updating any consumer. A failed save leaves the
		// old token usable; the next run reconciles orphan issuance through the API.
		if err = r.save(ctx, secret, saved); err != nil {
			return err
		}
	}
	if credential != saved.Previous.Value && credential != saved.Next.Value {
		return fmt.Errorf("source credential changed outside pending rotation")
	}
	verified, err := r.own(ctx, saved.Next.Value)
	if err != nil {
		return err
	}
	if verified.ID != saved.Next.ID || verified.UserID != saved.Previous.UserID || !sameScopes(verified.Scopes, saved.Previous.Scopes) {
		return fmt.Errorf("pending credential changed identity or scopes")
	}
	if err = r.distribute(ctx, saved); err != nil {
		return err
	}
	if err = r.verify(ctx, saved.Next.ID); err != nil {
		return err
	}
	// Re-scan after hot loading: a request that started with the old file may
	// have created a task Secret while kubelet was projecting the replacement.
	if err = r.distribute(ctx, saved); err != nil {
		return err
	}
	if saved.Cutover.IsZero() {
		saved.Cutover = r.now().UTC()
		saved.RetireAfter = saved.Cutover.Add(time.Duration(r.config.GraceSeconds) * time.Second)
		if err = r.save(ctx, secret, saved); err != nil {
			return err
		}
	}
	if r.now().Before(saved.RetireAfter) {
		slog.Info("GitLab credential loaded; overlap retained", "token_id", saved.Next.ID, "retire_after", saved.RetireAfter)
		return nil
	}
	if err = r.drain(ctx); err != nil {
		return err
	}
	var previous token
	if err = r.api(ctx, saved.Next.Value, "GET", fmt.Sprintf("/personal_access_tokens/%d", saved.Previous.ID), nil, &previous); err != nil {
		return err
	}
	if !previous.Revoked {
		if err = r.api(ctx, saved.Next.Value, "DELETE", fmt.Sprintf("/personal_access_tokens/%d", previous.ID), nil, nil); err != nil {
			return err
		}
	}
	id, expires := saved.Next.ID, saved.Next.ExpiresAt
	if err = r.save(ctx, secret, state{}); err != nil {
		return err
	}
	slog.Info("GitLab credential rotation complete", "token_id", id, "expires_at", expires)
	return nil
}

var errNotLoaded = errors.New("services have not loaded the replacement credential")
