package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

type Artifact struct {
	ExpectedHead string            `json:"expectedHead"`
	Tree         string            `json:"tree"`
	Changed      []string          `json:"changed"`
	Patch        string            `json:"patch"`
	Manifest     map[string]string `json:"manifest"`
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(
		ctx,
		"git",
		append([]string{"-c", "core.hooksPath=/dev/null", "-c", "credential.helper="}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git operation failed: %w: %s", err, string(out))
	}
	return string(out), nil
}

func allowed(p string, scopes []string) bool {
	if !path.IsAbs(p) && p != "" && !strings.HasPrefix(p, "../") {
		for _, scope := range scopes {
			scope = strings.TrimSuffix(scope, "/")
			if p == scope || strings.HasPrefix(p, scope+"/") {
				return true
			}
			match, _ := path.Match(scope, p)
			if match {
				return true
			}
		}
	}
	return false
}

func copyTree(source, destination string) error {
	return filepath.WalkDir(source, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		parts := strings.Split(rel, string(filepath.Separator))
		if parts[0] == ".git" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(destination, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if err = os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			resolved := filepath.Clean(filepath.Join(filepath.Dir(p), link))
			if filepath.IsAbs(link) ||
				!strings.HasPrefix(resolved, filepath.Clean(source)+string(filepath.Separator)) {
				return fmt.Errorf("source symlink escapes workspace: %s", rel)
			}
			return os.Symlink(link, target)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported repository file: %s", rel)
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
		if err != nil {
			return err
		}
		_, err = io.Copy(out, in)
		closeErr := out.Close()
		if err != nil {
			return err
		}
		return closeErr
	})
}

func CreateArtifact(
	ctx context.Context,
	source, candidate, repository, head string,
	scopes []string,
) (Artifact, error) {
	if _, err := os.Stat(candidate); err == nil {
		return Artifact{}, errors.New("candidate already exists; inspect saved artifact instead of overwriting")
	}
	if _, err := git(ctx, filepath.Dir(candidate), "clone", "--no-checkout", repository, candidate); err != nil {
		return Artifact{}, err
	}
	if _, err := git(ctx, candidate, "fetch", "origin", head); err != nil {
		return Artifact{}, err
	}
	if _, err := git(ctx, candidate, "checkout", "--detach", head); err != nil {
		return Artifact{}, err
	}
	entries, err := os.ReadDir(candidate)
	if err != nil {
		return Artifact{}, err
	}
	for _, entry := range entries {
		if entry.Name() != ".git" {
			if err = os.RemoveAll(filepath.Join(candidate, entry.Name())); err != nil {
				return Artifact{}, err
			}
		}
	}
	if err = copyTree(source, candidate); err != nil {
		return Artifact{}, err
	}
	if _, err = git(ctx, candidate, "add", "--all"); err != nil {
		return Artifact{}, err
	}
	changed, err := git(ctx, candidate, "diff", "--cached", "--name-only", "-z")
	if err != nil {
		return Artifact{}, err
	}
	result := Artifact{Changed: []string{}, Manifest: map[string]string{}}
	for _, p := range strings.Split(changed, "\x00") {
		if p == "" {
			continue
		}
		if !allowed(p, scopes) {
			return Artifact{}, fmt.Errorf("candidate change is outside allowed delivery paths: %s", p)
		}
		result.Changed = append(result.Changed, p)
	}
	result.Tree, err = git(ctx, candidate, "write-tree")
	if err != nil {
		return Artifact{}, err
	}
	result.Tree = strings.TrimSpace(result.Tree)
	if _, err = git(ctx, candidate, "clean", "-fdx"); err != nil {
		return Artifact{}, err
	}
	result.Patch, err = git(ctx, candidate, "diff", "--cached", "--binary")
	if err != nil {
		return Artifact{}, err
	}
	files, err := git(ctx, candidate, "ls-files", "-z")
	if err != nil {
		return Artifact{}, err
	}
	for _, p := range strings.Split(files, "\x00") {
		if p == "" {
			continue
		}
		hash, err := fileFingerprint(filepath.Join(candidate, p))
		if err != nil {
			return Artifact{}, err
		}
		result.Manifest[p] = hash
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return Artifact{}, err
	}
	if err = os.WriteFile(filepath.Join(filepath.Dir(candidate), "candidate.json"), raw, 0o600); err != nil {
		return Artifact{}, err
	}
	return result, nil
}

func fileFingerprint(p string) (string, error) {
	info, err := os.Lstat(p)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		link, err := os.Readlink(p)
		return "symlink:" + link, err
	}
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("executable=%t:%s", info.Mode()&0o100 != 0, hex.EncodeToString(h.Sum(nil))), nil
}

func CheckManifest(root string, manifest map[string]string) error {
	for p, expected := range manifest {
		if path.IsAbs(p) || hasParentSegment(p) || strings.HasPrefix(p, ".git/") {
			return fmt.Errorf("invalid artifact path: %s", p)
		}
		actual, err := fileFingerprint(filepath.Join(root, p))
		if err != nil {
			return err
		}
		if actual != expected {
			return fmt.Errorf("checks modified candidate source: %s", p)
		}
	}
	return nil
}

func hasParentSegment(p string) bool {
	for _, part := range strings.Split(filepath.ToSlash(p), "/") {
		if part == ".." {
			return true
		}
	}
	return false
}
