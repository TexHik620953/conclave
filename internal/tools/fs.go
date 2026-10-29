package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/texhik/conclave/internal/config"
	"github.com/texhik/conclave/internal/llm"
)

func schema(props map[string]any, required ...string) map[string]any {
	return map[string]any{
		"type": "object", "properties": props, "required": required,
		"additionalProperties": false,
	}
}

func prop(t, desc string) map[string]any {
	return map[string]any{"type": t, "description": desc}
}

type readFile struct{}

func (readFile) Name() string { return "read_file" }
func (readFile) Spec() llm.Tool {
	return llm.Tool{Name: "read_file", Description: "Read a text file. Prefer reading targeted ranges for large files.",
		Parameters: schema(map[string]any{
			"path":   prop("string", "File path, relative to the workspace"),
			"offset": prop("integer", "1-based line to start from"),
			"limit":  prop("integer", "Maximum number of lines"),
		}, "path")}
}
func (readFile) Run(ctx context.Context, env Env, args json.RawMessage) (string, error) {
	var in struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := input(args, &in); err != nil {
		return "", err
	}
	p, err := safePath(env.Cwd, in.Path, env.Permissions)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	text := string(b)
	if !utf8ish(text) {
		return "", fmt.Errorf("file %s looks binary", in.Path)
	}
	lines := strings.Split(text, "\n")
	start := in.Offset
	if start < 1 {
		start = 1
	}
	if start > len(lines) {
		return "", nil
	}
	limit := in.Limit
	if limit <= 0 || limit > 400 {
		limit = 400
	}
	end := start - 1 + limit
	if end > len(lines) {
		end = len(lines)
	}
	var bld strings.Builder
	for i := start - 1; i < end; i++ {
		fmt.Fprintf(&bld, "%6d\t%s\n", i+1, lines[i])
	}
	return clip(bld.String(), 80_000), nil
}

func utf8ish(s string) bool {
	return !strings.ContainsRune(s, 0)
}

type writeFile struct{}

func (writeFile) Name() string { return "write_file" }
func (writeFile) Spec() llm.Tool {
	return llm.Tool{Name: "write_file", Description: "Create or completely replace a text file.",
		Parameters: schema(map[string]any{
			"path":    prop("string", "File path, relative to the workspace"),
			"content": prop("string", "Full file content"),
		}, "path", "content")}
}
func (writeFile) Run(ctx context.Context, env Env, args json.RawMessage) (string, error) {
	if err := bound(env); err != nil {
		return "", err
	}
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := input(args, &in); err != nil {
		return "", err
	}
	p, err := safePath(env.Cwd, in.Path, env.Permissions)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(p, []byte(in.Content), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(in.Content), in.Path), nil
}

type editFile struct{}

func (editFile) Name() string { return "edit_file" }
func (editFile) Spec() llm.Tool {
	return llm.Tool{Name: "edit_file", Description: "Replace an exact text block in a file. old_string must occur exactly once unless replace_all is true.",
		Parameters: schema(map[string]any{
			"path":        prop("string", "File path, relative to the workspace"),
			"old_string":  prop("string", "Exact text to replace"),
			"new_string":  prop("string", "Replacement text"),
			"replace_all": prop("boolean", "Replace every occurrence"),
		}, "path", "old_string", "new_string")}
}
func (editFile) Run(ctx context.Context, env Env, args json.RawMessage) (string, error) {
	if err := bound(env); err != nil {
		return "", err
	}
	var in struct {
		Path       string `json:"path"`
		Old        string `json:"old_string"`
		New        string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := input(args, &in); err != nil {
		return "", err
	}
	if in.Old == "" {
		return "", errors.New("old_string is empty")
	}
	p, err := safePath(env.Cwd, in.Path, env.Permissions)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	s := string(b)
	count := strings.Count(s, in.Old)
	if count == 0 {
		return "", errors.New("old_string not found")
	}
	if count > 1 && !in.ReplaceAll {
		return "", fmt.Errorf("old_string occurs %d times; provide more context or set replace_all", count)
	}
	next := strings.ReplaceAll(s, in.Old, in.New)
	if err := os.WriteFile(p, []byte(next), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("edited %s (%d replacement(s))", in.Path, count), nil
}

type listDir struct{}

func (listDir) Name() string { return "list_dir" }
func (listDir) Spec() llm.Tool {
	return llm.Tool{Name: "list_dir", Description: "List files and directories.",
		Parameters: schema(map[string]any{"path": prop("string", "Directory path")}, "path")}
}
func (listDir) Run(ctx context.Context, env Env, args json.RawMessage) (string, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := input(args, &in); err != nil {
		return "", err
	}
	p, err := safePath(env.Cwd, in.Path, env.Permissions)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, e := range entries {
		if e.IsDir() {
			fmt.Fprintf(&b, "%s/\n", e.Name())
		} else {
			info, _ := e.Info()
			if info != nil {
				fmt.Fprintf(&b, "%s\t%d\n", e.Name(), info.Size())
			} else {
				fmt.Fprintln(&b, e.Name())
			}
		}
	}
	return clip(b.String(), 40_000), nil
}

type globTool struct{}

func (globTool) Name() string { return "glob" }
func (globTool) Spec() llm.Tool {
	return llm.Tool{Name: "glob", Description: "Find files by glob pattern. Supports **.",
		Parameters: schema(map[string]any{
			"pattern": prop("string", "Glob pattern, e.g. **/*.go"),
			"path":    prop("string", "Directory to search from"),
		}, "pattern")}
}
func (globTool) Run(ctx context.Context, env Env, args json.RawMessage) (string, error) {
	var in struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := input(args, &in); err != nil {
		return "", err
	}
	root, err := safePath(env.Cwd, in.Path, env.Permissions)
	if err != nil {
		return "", err
	}
	re, err := globRegexp(in.Pattern)
	if err != nil {
		return "", err
	}
	var out []string
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == ".conclave") {
			return filepath.SkipDir
		}
		rel, _ := filepath.Rel(root, p)
		if !d.IsDir() && re.MatchString(filepath.ToSlash(rel)) {
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(out)
	if len(out) > 500 {
		out = out[:500]
	}
	return strings.Join(out, "\n"), nil
}

func globRegexp(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	pattern = filepath.ToSlash(pattern)
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				b.WriteString(".*")
				i++
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(pattern[i])))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

type grepTool struct{}

func (grepTool) Name() string { return "grep" }
func (grepTool) Spec() llm.Tool {
	return llm.Tool{Name: "grep", Description: "Search file contents with a regular expression.",
		Parameters: schema(map[string]any{
			"pattern": prop("string", "Regular expression"),
			"path":    prop("string", "File or directory to search"),
			"include": prop("string", "Optional glob, e.g. *.go"),
		}, "pattern")}
}
func (grepTool) Run(ctx context.Context, env Env, args json.RawMessage) (string, error) {
	var in struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
		Include string `json:"include"`
	}
	if err := input(args, &in); err != nil {
		return "", err
	}
	root, err := safePath(env.Cwd, in.Path, env.Permissions)
	if err != nil {
		return "", err
	}
	re, err := regexp.Compile(in.Pattern)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	matches := 0
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || ctx.Err() != nil {
			return nil
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == ".conclave") {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		if in.Include != "" {
			if ok, _ := filepath.Match(in.Include, d.Name()); !ok {
				return nil
			}
		}
		if info, err := d.Info(); err != nil || info.Size() > 2_000_000 {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil || !utf8ish(string(data)) {
			return nil
		}
		rel, _ := filepath.Rel(env.Cwd, p)
		for i, line := range strings.Split(string(data), "\n") {
			if re.MatchString(line) {
				fmt.Fprintf(&b, "%s:%d:%s\n", filepath.ToSlash(rel), i+1, clip(line, 300))
				matches++
				if matches >= 400 {
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if b.Len() == 0 {
		return "no matches", nil
	}
	return clip(b.String(), 60_000), nil
}

var _ = bytes.MinRead
var _ = config.Permissions{}
