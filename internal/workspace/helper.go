package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type PublishReceipt struct {
	Head    string `json:"head"`
	Tree    string `json:"tree"`
	Patch   string `json:"patch"`
	Changed bool   `json:"changed"`
}

func Helper(ctx context.Context, phase string) error {
	// The helper executes fixed platform code. Repository code runs only in the
	// separate verifier, which receives neither of these credentials.
	askpass := filepath.Join("/scratch", "askpass")
	if err := os.WriteFile(askpass, []byte("#!/bin/sh\ncase \"$1\" in *Username*) printf '%s\\n' oauth2;; *) printf '%s\\n' \"$GITLAB_TOKEN\";; esac\n"), 0700); err != nil {
		return err
	}
	if err := os.Setenv("GIT_ASKPASS", askpass); err != nil {
		return err
	}
	defer os.Remove(askpass)
	var scopes []string
	if err := json.Unmarshal([]byte(os.Getenv("ALLOWED_PATHS")), &scopes); err != nil {
		return err
	}
	candidate := "/volume/candidate"
	switch phase {
	case "snapshot":
		artifact, err := CreateArtifact(ctx, "/source/repo", candidate, os.Getenv("REPOSITORY_URL"), os.Getenv("BASE_HEAD"), scopes)
		if err != nil {
			return err
		}
		os.Setenv("GIT_AUTHOR_DATE", os.Getenv("COMMIT_DATE"))
		os.Setenv("GIT_COMMITTER_DATE", os.Getenv("COMMIT_DATE"))
		commit, err := git(ctx, candidate, "-c", "user.name=Forge Coding Agent", "-c", "user.email=agent@forge.local", "commit-tree", artifact.Tree, "-p", os.Getenv("BASE_HEAD"), "-m", os.Getenv("SUMMARY"))
		if err != nil {
			return err
		}
		artifact.ExpectedHead = strings.TrimSpace(commit)
		raw, err := json.Marshal(artifact)
		if err != nil {
			return err
		}
		if err = os.WriteFile("/volume/candidate.json", raw, 0600); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(artifact)
	case "publish":
		raw, err := os.ReadFile("/volume/candidate.json")
		if err != nil {
			return err
		}
		var artifact Artifact
		if err = json.Unmarshal(raw, &artifact); err != nil {
			return err
		}
		if err = CheckManifest("/verified/repo", artifact.Manifest); err != nil {
			return err
		}
		if err = CheckManifest(candidate, artifact.Manifest); err != nil {
			return err
		}
		tree, err := git(ctx, candidate, "write-tree")
		if err != nil {
			return err
		}
		if strings.TrimSpace(tree) != artifact.Tree {
			return errors.New("candidate tree changed before publication")
		}
		receipt := PublishReceipt{Tree: artifact.Tree, Patch: artifact.Patch, Changed: len(artifact.Changed) > 0}
		if !receipt.Changed {
			receipt.Head = os.Getenv("BASE_HEAD")
			if err = syncSourceGit(ctx, candidate, receipt.Head); err != nil {
				return err
			}
			return json.NewEncoder(os.Stdout).Encode(receipt)
		}
		authorDate := os.Getenv("COMMIT_DATE")
		if authorDate == "" {
			return errors.New("stable commit date is required")
		}
		os.Setenv("GIT_AUTHOR_DATE", authorDate)
		os.Setenv("GIT_COMMITTER_DATE", authorDate)
		head, err := git(ctx, candidate, "-c", "user.name=Forge Coding Agent", "-c", "user.email=agent@forge.local", "commit-tree", artifact.Tree, "-p", os.Getenv("BASE_HEAD"), "-m", os.Getenv("SUMMARY"))
		if err != nil {
			return err
		}
		receipt.Head = strings.TrimSpace(head)
		// Check permission immediately before the external write. A request already
		// sent to GitLab still needs reconciliation if cancellation races with it.
		req, err := http.NewRequestWithContext(ctx, "POST", os.Getenv("PERMIT_URL"), strings.NewReader(`{}`))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+os.Getenv("CODING_TOKEN"))
		req.Header.Set("Content-Type", "application/json")
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		response, err := (&http.Client{Transport: transport, Timeout: 15 * time.Second}).Do(req)
		if err != nil {
			return err
		}
		response.Body.Close()
		if response.StatusCode != 200 {
			return fmt.Errorf("publication permission rejected: HTTP %d", response.StatusCode)
		}
		branch := os.Getenv("WORK_BRANCH")
		remote, err := git(ctx, candidate, "ls-remote", "origin", "refs/heads/"+branch)
		if err != nil {
			return err
		}
		fields := strings.Fields(remote)
		if len(fields) > 0 && fields[0] == receipt.Head {
			if err = syncSourceGit(ctx, candidate, receipt.Head); err != nil {
				return err
			}
			return json.NewEncoder(os.Stdout).Encode(receipt)
		}
		expected := os.Getenv("EXPECTED_REMOTE_HEAD")
		if len(fields) == 0 && expected != "" || len(fields) > 0 && fields[0] != expected {
			return errors.New("remote branch changed; human commit has priority")
		}
		if _, err = git(ctx, candidate, "push", "--force-with-lease=refs/heads/"+branch+":"+expected, "origin", receipt.Head+":refs/heads/"+branch); err != nil {
			return err
		}
		if err = syncSourceGit(ctx, candidate, receipt.Head); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(receipt)
	default:
		return errors.New("unknown workspace helper phase")
	}
}

// Compute is paused during publication. Replace untrusted Git metadata with
// the platform's fresh repository so the next round sees the published HEAD.
func syncSourceGit(ctx context.Context, candidate, head string) error {
	if _, err := git(ctx, candidate, "symbolic-ref", "HEAD", "refs/heads/"+os.Getenv("WORK_BRANCH")); err != nil {
		return err
	}
	if _, err := git(ctx, candidate, "update-ref", "HEAD", head); err != nil {
		return err
	}
	temporary, err := os.MkdirTemp("/source", ".forge-git-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	if err = copyTree(filepath.Join(candidate, ".git"), temporary); err != nil {
		return err
	}
	if err = os.RemoveAll("/source/repo/.git"); err != nil {
		return err
	}
	if err = os.Rename(temporary, "/source/repo/.git"); err != nil {
		return err
	}
	return nil
}
