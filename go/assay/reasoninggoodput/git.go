package reasoninggoodput

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// git.go: Git witness adapter over a REAL temporary repository. Witnesses
// are genuine Git object identities (blob OIDs for file content, commit OIDs
// for snapshots) produced by the git CLI. Nothing is replaced by a Thompson
// sequence number. Head movement vs per-blob movement are detected
// independently.

// GitRepo is a temporary Git repository fixture.
type GitRepo struct {
	Dir string
}

func git(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

func gitIn(dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir}, args...)
	return git(full...)
}

// gitBytes runs git without trimming (exact content reads).
func gitBytes(dir string, args ...string) ([]byte, error) {
	full := append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", full...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %v: %w", full, err)
	}
	return out, nil
}

// NewGitRepo creates a repository with an initial commit.
func NewGitRepo(t TmpDir) (*GitRepo, error) {
	dir := t.TempDir()
	if _, err := gitIn(dir, "init", "-q"); err != nil {
		return nil, err
	}
	if _, err := gitIn(dir, "config", "user.email", "assay@test"); err != nil {
		return nil, err
	}
	if _, err := gitIn(dir, "config", "user.name", "assay"); err != nil {
		return nil, err
	}
	return &GitRepo{Dir: dir}, nil
}

// TmpDir abstracts *testing.T for TempDir (keeps assay code test-adjacent
// without importing testing into non-test files... actually testing.TempDir
// needs *testing.T; define minimal interface here).
type TmpDir interface {
	TempDir() string
}

// WriteFile writes path (repo-relative) with content.
func (g *GitRepo) WriteFile(path, content string) error {
	full := filepath.Join(g.Dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, []byte(content), 0o644)
}

// Commit stages everything and commits; returns the commit OID.
func (g *GitRepo) Commit(msg string) (string, error) {
	if _, err := gitIn(g.Dir, "add", "-A"); err != nil {
		return "", err
	}
	if _, err := gitIn(g.Dir, "commit", "-qm", msg); err != nil {
		return "", err
	}
	return gitIn(g.Dir, "rev-parse", "HEAD")
}

// Head returns the current HEAD commit OID.
func (g *GitRepo) Head() (string, error) {
	return gitIn(g.Dir, "rev-parse", "HEAD")
}

// BlobOID returns the blob OID for repo-relative path at HEAD.
func (g *GitRepo) BlobOID(path string) (string, error) {
	return gitIn(g.Dir, "rev-parse", "HEAD:"+path)
}

// WorktreeBytes reads current working-tree bytes (may differ from witness).
func (g *GitRepo) WorktreeBytes(path string) ([]byte, error) {
	return os.ReadFile(filepath.Join(g.Dir, path))
}

// ReadWitnessed reads path at HEAD, binding value to its blob witness
// (exact bytes via cat-file, never trimmed).
func (g *GitRepo) ReadWitnessed(path string) (WitnessedRead, error) {
	oid, err := g.BlobOID(path)
	if err != nil {
		return WitnessedRead{}, err
	}
	out, err := gitBytes(g.Dir, "cat-file", "-p", "HEAD:"+path)
	if err != nil {
		return WitnessedRead{}, err
	}
	return WitnessedRead{
		Value: out,
		Witness: StateWitness{
			Ref:        ResourceRef{Authority: "git", ID: path},
			Version:    oid,
			ObservedAt: nowRFC3339(),
		},
		Path: path,
	}, nil
}

// Close removes the repository.
func (g *GitRepo) Close() error { return os.RemoveAll(g.Dir) }
