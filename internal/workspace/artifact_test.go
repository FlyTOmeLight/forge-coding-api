package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCandidateUsesTrustedGitAndChecksAllDeliveryPaths(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	origin := filepath.Join(root, "origin")
	source := filepath.Join(root, "source")
	candidate := filepath.Join(root, "candidate")
	if err := os.MkdirAll(origin, 0755); err != nil {
		t.Fatal(err)
	}
	command := func(dir string, args ...string) string {
		t.Helper()
		out, err := git(ctx, dir, args...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	command(origin, "init")
	command(origin, "config", "user.email", "test@local")
	command(origin, "config", "user.name", "test")
	os.WriteFile(filepath.Join(origin, "calc.py"), []byte("original\n"), 0644)
	os.WriteFile(filepath.Join(origin, "deleted.py"), []byte("old\n"), 0644)
	command(origin, "add", ".")
	command(origin, "commit", "-m", "base")
	head := command(origin, "rev-parse", "HEAD")
	command(root, "clone", origin, source)
	os.WriteFile(filepath.Join(source, "calc.py"), []byte("changed\n"), 0644)
	os.Remove(filepath.Join(source, "deleted.py"))
	os.WriteFile(filepath.Join(source, "new.py"), []byte("new\n"), 0755)
	// The source's git config must never determine trusted collection behavior.
	os.WriteFile(filepath.Join(source, ".git", "config"), []byte("not valid Git config"), 0644)
	// The helper's working directory is provided by its trusted Job.

	artifact, err := CreateArtifact(ctx, source, candidate, origin, stringTrim(head), []string{"*.py"})
	if err != nil {
		t.Fatal(err)
	}
	if len(artifact.Changed) != 3 || artifact.Tree == "" || artifact.Manifest["new.py"] == "" {
		t.Fatalf("artifact: %+v", artifact)
	}
	if err = CheckManifest(candidate, artifact.Manifest); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(candidate, "calc.py"), []byte("changed by checks\n"), 0644)
	if err = CheckManifest(candidate, artifact.Manifest); err == nil {
		t.Fatal("source mutation was accepted")
	}
	denied := filepath.Join(root, "denied")
	if _, err = CreateArtifact(ctx, source, denied, origin, stringTrim(head), []string{"calc.py"}); err == nil {
		t.Fatal("out-of-scope addition/deletion was accepted")
	}
}
func stringTrim(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

func TestManifestTracksGitExecutableBitNotMountPermissions(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "source.py")
	if err := os.WriteFile(file, []byte("print(1)\n"), 0644); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := fileFingerprint(file)
	if err != nil {
		t.Fatal(err)
	}
	manifest := map[string]string{"source.py": fingerprint}
	if err = os.Chmod(file, 0664); err != nil {
		t.Fatal(err)
	}
	if err = CheckManifest(root, manifest); err != nil {
		t.Fatalf("volume group-write permission is not a Git change: %v", err)
	}
	if err = os.Chmod(file, 0755); err != nil {
		t.Fatal(err)
	}
	if err = CheckManifest(root, manifest); err == nil {
		t.Fatal("executable bit change was ignored")
	}
}
