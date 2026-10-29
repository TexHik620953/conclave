package gitx

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Repo struct {
	Dir string
}

func (r Repo) git(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.Dir
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	if err != nil {
		return out.String(), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

func (r Repo) IsRepo(ctx context.Context) bool {
	_, err := r.git(ctx, "rev-parse", "--is-inside-work-tree")
	return err == nil
}

func (r Repo) Head(ctx context.Context) (string, error) {
	out, err := r.git(ctx, "rev-parse", "HEAD")
	return strings.TrimSpace(out), err
}

func (r Repo) Dirty(ctx context.Context) (bool, error) {
	out, err := r.git(ctx, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// Snapshot creates a hidden commit of the current working tree, including
// untracked files, without modifying the real index or working tree.
func (r Repo) Snapshot(ctx context.Context) (string, error) {
	dirty, err := r.Dirty(ctx)
	if err != nil {
		return "", err
	}
	if !dirty {
		return r.Head(ctx)
	}
	head, err := r.Head(ctx)
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp("", "conclave-index-*")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	os.Remove(tmpPath)
	defer os.Remove(tmpPath)
	env := append(os.Environ(),
		"GIT_INDEX_FILE="+tmpPath,
		"GIT_AUTHOR_NAME=conclave", "GIT_AUTHOR_EMAIL=conclave@localhost",
		"GIT_COMMITTER_NAME=conclave", "GIT_COMMITTER_EMAIL=conclave@localhost",
	)
	run := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = r.Dir
		cmd.Env = env
		var out, errb bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &errb
		if err := cmd.Run(); err != nil {
			return out.String(), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
		}
		return out.String(), nil
	}
	if _, err := run("read-tree", head); err != nil {
		return "", err
	}
	if _, err := run("add", "-A"); err != nil {
		return "", err
	}
	tree, err := run("write-tree")
	if err != nil {
		return "", err
	}
	headTree, err := r.git(ctx, "rev-parse", head+"^{tree}")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(tree) == strings.TrimSpace(headTree) {
		return head, nil
	}
	commit, err := run("commit-tree", strings.TrimSpace(tree), "-p", head, "-m", "conclave: workspace snapshot")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(commit), nil
}

func (r Repo) WorktreeAdd(ctx context.Context, dir, branch, base string) error {
	_, err := r.git(ctx, "worktree", "add", "-b", branch, dir, base)
	return err
}

func (r Repo) WorktreeRemove(ctx context.Context, dir string, force bool) error {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, dir)
	_, err := r.git(ctx, args...)
	return err
}

func (r Repo) PruneWorktrees(ctx context.Context) error {
	_, err := r.git(ctx, "worktree", "prune")
	return err
}

func (r Repo) CommitAll(ctx context.Context, message string) (string, error) {
	if _, err := r.git(ctx, "add", "-A"); err != nil {
		return "", err
	}
	out, err := r.git(ctx, "status", "--porcelain")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out) == "" {
		h, err := r.Head(ctx)
		return h, err
	}
	if _, err := r.git(ctx, "commit", "--no-gpg-sign", "-m", message); err != nil {
		return "", err
	}
	return r.Head(ctx)
}

func (r Repo) Merge(ctx context.Context, branch, message string) error {
	_, err := r.git(ctx, "merge", "--no-ff", "--no-gpg-sign", branch, "-m", message)
	if err != nil {
		_, _ = r.git(ctx, "merge", "--abort")
	}
	return err
}

func (r Repo) Diff(ctx context.Context, base string) (string, error) {
	out, err := r.git(ctx, "diff", "--stat", "--patch", base)
	return out, err
}

func (r Repo) CheckoutMerge(ctx context.Context, branch string) error {
	dirty, err := r.Dirty(ctx)
	if err != nil {
		return err
	}
	if dirty {
		return fmt.Errorf("workspace has uncommitted changes; commit or stash them before /apply")
	}
	_, err = r.git(ctx, "merge", "--no-ff", "--no-gpg-sign", branch, "-m", "conclave: apply "+branch)
	return err
}

func (r Repo) BranchExists(ctx context.Context, branch string) bool {
	_, err := r.git(ctx, "rev-parse", "--verify", branch)
	return err == nil
}

func (r Repo) RemoveBranch(ctx context.Context, branch string) error {
	_, err := r.git(ctx, "branch", "-D", branch)
	return err
}

func (r Repo) TopLevel(ctx context.Context) (string, error) {
	out, err := r.git(ctx, "rev-parse", "--show-toplevel")
	return strings.TrimSpace(out), err
}

func Cwd() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}

// DiscoverRepos returns the workspace root when it is a git repository, or
// immediate child directories that are repositories. Group workspaces often
// contain several independent projects rather than one repository at the root.
func DiscoverRepos(ctx context.Context, root string) []string {
	if (Repo{Dir: root}).IsRepo(ctx) {
		return []string{root}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var repos []string
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join(root, entry.Name())
		if (Repo{Dir: path}).IsRepo(ctx) {
			repos = append(repos, path)
		}
	}
	return repos
}

// ResolveRepo chooses the repository for a task. Hints are usually write
// scopes, component and task text; the first unique directory match wins.
func ResolveRepo(ctx context.Context, root string, hints ...string) (string, error) {
	repos := DiscoverRepos(ctx, root)
	if len(repos) == 0 {
		return "", fmt.Errorf("no git repository found in workspace %s or its immediate subdirectories", root)
	}
	if len(repos) == 1 {
		return repos[0], nil
	}
	best := ""
	bestScore := -1
	for hintIndex, hint := range hints {
		h := filepath.ToSlash(strings.TrimSpace(strings.ToLower(hint)))
		if h == "" {
			continue
		}
		// Earlier hints are stronger (write scope, component, title, text).
		weight := len(hints) - hintIndex
		for _, repo := range repos {
			for _, alias := range repoAliases(filepath.Base(repo)) {
				idx := wordIndex(h, alias)
				if idx < 0 {
					continue
				}
				score := weight*1_000_000_000 - idx*1000 + len(alias)
				if score > bestScore {
					best, bestScore = repo, score
				}
			}
		}
	}
	if best != "" {
		return best, nil
	}
	return repos[0], nil
}

// repoAliases lets a task refer to "node-go" for a repository named
// "liberator-node-go" without relying on the common project prefix.
func repoAliases(name string) []string {
	name = strings.ToLower(name)
	parts := strings.Split(name, "-")
	aliasSet := map[string]bool{name: true}
	for i := 1; i < len(parts); i++ {
		alias := strings.Join(parts[i:], "-")
		if len(alias) >= 3 {
			aliasSet[alias] = true
		}
	}
	out := make([]string, 0, len(aliasSet))
	for alias := range aliasSet {
		out = append(out, alias)
	}
	return out
}

func wordIndex(haystack, needle string) int {
	if needle == "" {
		return -1
	}
	offset := 0
	for {
		idx := strings.Index(haystack[offset:], needle)
		if idx < 0 {
			return -1
		}
		idx += offset
		beforeOK := idx == 0 || !isWordByte(haystack[idx-1])
		end := idx + len(needle)
		afterOK := end == len(haystack) || !isWordByte(haystack[end])
		if beforeOK && afterOK {
			return idx
		}
		offset = idx + 1
	}
}

func isWordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_'
}

func (r Repo) CurrentBranch(ctx context.Context) (string, error) {
	out, err := r.git(ctx, "rev-parse", "--abbrev-ref", "HEAD")
	return strings.TrimSpace(out), err
}
