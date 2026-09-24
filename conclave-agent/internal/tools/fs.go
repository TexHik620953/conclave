package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

type readFile struct{ r *Registry }

func (t *readFile) Name() string        { return "read_file" }
func (t *readFile) Description() string { return "Read a UTF-8 text file from the workspace." }
func (t *readFile) Schema() json.RawMessage {
	return schema(map[string]any{
		"path":   map[string]any{"type": "string"},
		"offset": map[string]any{"type": "integer", "description": "1-based first line."},
		"limit":  map[string]any{"type": "integer", "description": "Max lines."},
	}, "path")
}

func (t *readFile) Execute(_ context.Context, args json.RawMessage) (Result, error) {
	var in struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return Result{}, err
	}
	p, err := t.r.resolvePath(in.Path)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	f, err := os.Open(p)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	defer f.Close()
	var b strings.Builder
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	line := 0
	written := 0
	for sc.Scan() {
		line++
		if in.Offset > 0 && line < in.Offset {
			continue
		}
		if in.Limit > 0 && written >= in.Limit {
			break
		}
		fmt.Fprintf(&b, "%d: %s\n", line, sc.Text())
		written++
	}
	return Result{Content: b.String()}, sc.Err()
}

type writeFile struct{ r *Registry }

func (t *writeFile) Name() string        { return "write_file" }
func (t *writeFile) Description() string { return "Create or overwrite a file in the workspace." }
func (t *writeFile) Schema() json.RawMessage {
	return schema(map[string]any{
		"path":    map[string]any{"type": "string"},
		"content": map[string]any{"type": "string"},
	}, "path", "content")
}

func (t *writeFile) Execute(_ context.Context, args json.RawMessage) (Result, error) {
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return Result{}, err
	}
	p, err := t.r.resolvePath(in.Path)
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

type editFile struct{ r *Registry }

func (t *editFile) Name() string        { return "edit_file" }
func (t *editFile) Description() string { return "Replace the first occurrence of a string in a file." }
func (t *editFile) Schema() json.RawMessage {
	return schema(map[string]any{
		"path": map[string]any{"type": "string"},
		"old":  map[string]any{"type": "string"},
		"new":  map[string]any{"type": "string"},
	}, "path", "old", "new")
}

func (t *editFile) Execute(_ context.Context, args json.RawMessage) (Result, error) {
	var in struct {
		Path string `json:"path"`
		Old  string `json:"old"`
		New  string `json:"new"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return Result{}, err
	}
	p, err := t.r.resolvePath(in.Path)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	if !strings.Contains(string(data), in.Old) {
		return Result{Content: "old string not found", IsError: true}, nil
	}
	out := strings.Replace(string(data), in.Old, in.New, 1)
	if err := os.WriteFile(p, []byte(out), 0o644); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	return Result{Content: "edited " + in.Path}, nil
}

type listDir struct{ r *Registry }

func (t *listDir) Name() string        { return "list_dir" }
func (t *listDir) Description() string { return "List directory entries in the workspace." }
func (t *listDir) Schema() json.RawMessage {
	return schema(map[string]any{"path": map[string]any{"type": "string"}}, "path")
}

func (t *listDir) Execute(_ context.Context, args json.RawMessage) (Result, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return Result{}, err
	}
	p, err := t.r.resolvePath(in.Path)
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
	return Result{Content: b.String()}, nil
}

type globTool struct{ r *Registry }

func (t *globTool) Name() string        { return "glob" }
func (t *globTool) Description() string { return "Find files by glob pattern (supports **)." }
func (t *globTool) Schema() json.RawMessage {
	return schema(map[string]any{"pattern": map[string]any{"type": "string"}}, "pattern")
}

func (t *globTool) Execute(_ context.Context, args json.RawMessage) (Result, error) {
	var in struct {
		Pattern string `json:"pattern"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return Result{}, err
	}
	root := t.r.workspace
	pattern := filepath.ToSlash(in.Pattern)
	if filepath.IsAbs(in.Pattern) || strings.HasPrefix(pattern, "../") {
		return Result{Content: "pattern must be relative", IsError: true}, nil
	}
	var out []string
	if !strings.Contains(pattern, "**") {
		matches, _ := filepath.Glob(filepath.Join(root, filepath.FromSlash(pattern)))
		out = matches
	} else {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(root, p)
			if err == nil && matchGlob(pattern, filepath.ToSlash(rel)) {
				out = append(out, p)
			}
			return nil
		})
	}
	var b strings.Builder
	for _, m := range out {
		if rel, err := filepath.Rel(root, m); err == nil {
			fmt.Fprintln(&b, rel)
		}
	}
	if b.Len() == 0 {
		return Result{Content: "no matches"}, nil
	}
	return Result{Content: b.String()}, nil
}

type grepTool struct{ r *Registry }

func (t *grepTool) Name() string        { return "grep" }
func (t *grepTool) Description() string { return "Search file contents by regular expression." }
func (t *grepTool) Schema() json.RawMessage {
	return schema(map[string]any{
		"pattern": map[string]any{"type": "string"},
		"path":    map[string]any{"type": "string"},
		"include": map[string]any{"type": "string", "description": "Glob for filenames, e.g. *.go"},
	}, "pattern")
}

func (t *grepTool) Execute(_ context.Context, args json.RawMessage) (Result, error) {
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
		return Result{Content: "bad regexp: " + err.Error(), IsError: true}, nil
	}
	root := t.r.workspace
	start := root
	if in.Path != "" {
		start, err = t.r.resolvePath(in.Path)
		if err != nil {
			return Result{Content: err.Error(), IsError: true}, nil
		}
	}
	var b strings.Builder
	count := 0
	_ = filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || count > 500 {
			return nil
		}
		if in.Include != "" {
			if ok, _ := path.Match(in.Include, d.Name()); !ok {
				return nil
			}
		}
		data, err := os.ReadFile(p)
		if err != nil || len(data) > 2*1024*1024 {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		for i, line := range strings.Split(string(data), "\n") {
			if re.MatchString(line) {
				fmt.Fprintf(&b, "%s:%d: %s\n", rel, i+1, strings.TrimSpace(line))
				count++
			}
		}
		return nil
	})
	if b.Len() == 0 {
		return Result{Content: "no matches"}, nil
	}
	return Result{Content: b.String()}, nil
}

// matchGlob matches a slash-separated glob where ** spans path segments.
func matchGlob(pattern, name string) bool {
	pp := strings.Split(pattern, "/")
	np := strings.Split(name, "/")
	var match func(i, j int) bool
	match = func(i, j int) bool {
		for i < len(pp) {
			if pp[i] == "**" {
				for i < len(pp) && pp[i] == "**" {
					i++
				}
				if i == len(pp) {
					return true
				}
				for k := j; k <= len(np); k++ {
					if match(i, k) {
						return true
					}
				}
				return false
			}
			if j >= len(np) {
				return false
			}
			if ok, _ := path.Match(pp[i], np[j]); !ok {
				return false
			}
			i++
			j++
		}
		return j == len(np)
	}
	return match(0, 0)
}
