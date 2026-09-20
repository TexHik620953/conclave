package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func resolvePath(workspace, p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("path is required")
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(workspace, p)
	}
	p = filepath.Clean(p)
	ws := filepath.Clean(workspace)
	if ws != "" && p != ws && !strings.HasPrefix(p, ws+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q escapes workspace %q", p, ws)
	}
	return p, nil
}

func truncate(env *Env, s string) string {
	max := env.MaxOutputBytes
	if max <= 0 {
		max = 64 * 1024
	}
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n... [truncated %d bytes]", len(s)-max)
}

type readFile struct{ env *Env }

func (t *readFile) Name() string { return "read_file" }
func (t *readFile) Description() string {
	return "Read a UTF-8 text file from the workspace. Supports optional line offset and limit."
}
func (t *readFile) Schema() json.RawMessage {
	return schema(map[string]any{
		"path":   map[string]any{"type": "string", "description": "Path relative to the workspace."},
		"offset": map[string]any{"type": "integer", "description": "1-based first line to read."},
		"limit":  map[string]any{"type": "integer", "description": "Maximum number of lines."},
	}, "path")
}

func (t *readFile) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	if !t.env.FSRead {
		return Result{Content: "fs_read permission denied", IsError: true}, nil
	}
	var in struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return Result{}, err
	}
	p, err := resolvePath(t.env.Workspace, in.Path)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	lines := strings.Split(string(data), "\n")
	start := 0
	if in.Offset > 1 {
		start = in.Offset - 1
	}
	if start > len(lines) {
		start = len(lines)
	}
	end := len(lines)
	if in.Limit > 0 && start+in.Limit < end {
		end = start + in.Limit
	}
	var b strings.Builder
	for i := start; i < end; i++ {
		fmt.Fprintf(&b, "%d: %s\n", i+1, lines[i])
	}
	return Result{Content: truncate(t.env, b.String())}, nil
}

type writeFile struct{ env *Env }

func (t *writeFile) Name() string { return "write_file" }
func (t *writeFile) Description() string {
	return "Create or overwrite a file in the workspace with the given content."
}
func (t *writeFile) Schema() json.RawMessage {
	return schema(map[string]any{
		"path":    map[string]any{"type": "string"},
		"content": map[string]any{"type": "string"},
	}, "path", "content")
}

func (t *writeFile) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	if !t.env.FSWrite {
		return Result{Content: "fs_write permission denied", IsError: true}, nil
	}
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return Result{}, err
	}
	p, err := resolvePath(t.env.Workspace, in.Path)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	if err := os.WriteFile(p, []byte(in.Content), 0o644); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	return Result{Content: fmt.Sprintf("wrote %d bytes to %s", len(in.Content), in.Path)}, nil
}

type editFile struct{ env *Env }

func (t *editFile) Name() string { return "edit_file" }
func (t *editFile) Description() string {
	return "Replace an exact substring in a file. Fails if the target is absent or ambiguous unless replace_all is true."
}
func (t *editFile) Schema() json.RawMessage {
	return schema(map[string]any{
		"path":        map[string]any{"type": "string"},
		"old":         map[string]any{"type": "string", "description": "Exact text to replace."},
		"new":         map[string]any{"type": "string", "description": "Replacement text."},
		"replace_all": map[string]any{"type": "boolean"},
	}, "path", "old", "new")
}

func (t *editFile) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	if !t.env.FSWrite {
		return Result{Content: "fs_write permission denied", IsError: true}, nil
	}
	var in struct {
		Path       string `json:"path"`
		Old        string `json:"old"`
		New        string `json:"new"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return Result{}, err
	}
	p, err := resolvePath(t.env.Workspace, in.Path)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	content := string(data)
	count := strings.Count(content, in.Old)
	if count == 0 {
		return Result{Content: "target text not found", IsError: true}, nil
	}
	if count > 1 && !in.ReplaceAll {
		return Result{Content: fmt.Sprintf("target text occurs %d times; set replace_all or provide more context", count), IsError: true}, nil
	}
	var updated string
	if in.ReplaceAll {
		updated = strings.ReplaceAll(content, in.Old, in.New)
	} else {
		updated = strings.Replace(content, in.Old, in.New, 1)
	}
	if err := os.WriteFile(p, []byte(updated), 0o644); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	return Result{Content: fmt.Sprintf("replaced %d occurrence(s) in %s", count, in.Path)}, nil
}

type listDir struct{ env *Env }

func (t *listDir) Name() string { return "list_dir" }
func (t *listDir) Description() string {
	return "List entries of a directory in the workspace."
}
func (t *listDir) Schema() json.RawMessage {
	return schema(map[string]any{
		"path": map[string]any{"type": "string", "description": "Directory relative to the workspace; defaults to root."},
	})
}

func (t *listDir) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	if !t.env.FSRead {
		return Result{Content: "fs_read permission denied", IsError: true}, nil
	}
	var in struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return Result{}, err
	}
	if in.Path == "" {
		in.Path = "."
	}
	p, err := resolvePath(t.env.Workspace, in.Path)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	var b strings.Builder
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		fmt.Fprintln(&b, name)
	}
	return Result{Content: truncate(t.env, b.String())}, nil
}

type globTool struct{ env *Env }

func (t *globTool) Name() string { return "glob" }
func (t *globTool) Description() string {
	return "Find files matching a glob pattern relative to the workspace (e.g. **/*.go)."
}
func (t *globTool) Schema() json.RawMessage {
	return schema(map[string]any{
		"pattern": map[string]any{"type": "string"},
	}, "pattern")
}

func (t *globTool) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	if !t.env.FSRead {
		return Result{Content: "fs_read permission denied", IsError: true}, nil
	}
	var in struct {
		Pattern string `json:"pattern"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return Result{}, err
	}
	root := t.env.Workspace
	if root == "" {
		root = "."
	}
	matches, err := filepath.Glob(filepath.Join(root, in.Pattern))
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	var b strings.Builder
	for _, m := range matches {
		if rel, err := filepath.Rel(root, m); err == nil {
			fmt.Fprintln(&b, rel)
		}
	}
	if b.Len() == 0 {
		return Result{Content: "no matches"}, nil
	}
	return Result{Content: truncate(t.env, b.String())}, nil
}

type grepTool struct{ env *Env }

func (t *grepTool) Name() string { return "grep" }
func (t *grepTool) Description() string {
	return "Search file contents with a regular expression under the workspace."
}
func (t *grepTool) Schema() json.RawMessage {
	return schema(map[string]any{
		"pattern": map[string]any{"type": "string", "description": "RE2 regular expression."},
		"path":    map[string]any{"type": "string", "description": "Directory to search; defaults to workspace root."},
		"include": map[string]any{"type": "string", "description": "Glob for filenames, e.g. *.go."},
	}, "pattern")
}

func (t *grepTool) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	if !t.env.FSRead {
		return Result{Content: "fs_read permission denied", IsError: true}, nil
	}
	var in struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
		Include string `json:"include"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return Result{}, err
	}
	re, err := regexp.Compile(in.Pattern)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	root := t.env.Workspace
	if in.Path != "" {
		root, err = resolvePath(t.env.Workspace, in.Path)
		if err != nil {
			return Result{Content: err.Error(), IsError: true}, nil
		}
	}
	if root == "" {
		root = "."
	}
	var b strings.Builder
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if in.Include != "" {
			if ok, _ := filepath.Match(in.Include, d.Name()); !ok {
				return nil
			}
		}
		data, err := os.ReadFile(path)
		if err != nil || len(data) > 2*1024*1024 {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		for i, line := range strings.Split(string(data), "\n") {
			if re.MatchString(line) {
				fmt.Fprintf(&b, "%s:%d: %s\n", rel, i+1, strings.TrimSpace(line))
				if b.Len() > 256*1024 {
					return fmt.Errorf("stop")
				}
			}
		}
		return nil
	})
	if b.Len() == 0 {
		return Result{Content: "no matches"}, nil
	}
	return Result{Content: truncate(t.env, b.String())}, nil
}
