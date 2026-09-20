package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type patchHunk struct {
	oldStart int
	lines    []string
}

type filePatch struct {
	path  string
	hunks []patchHunk
}

type applyPatch struct{ env *Env }

func (t *applyPatch) Name() string { return "apply_patch" }
func (t *applyPatch) Description() string {
	return "Apply a unified diff to one or more files in the workspace."
}
func (t *applyPatch) Schema() json.RawMessage {
	return schema(map[string]any{
		"patch": map[string]any{"type": "string", "description": "Unified diff text."},
	}, "patch")
}

func (t *applyPatch) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	if !t.env.FSWrite {
		return Result{Content: "fs_write permission denied", IsError: true}, nil
	}
	var in struct {
		Patch string `json:"patch"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return Result{}, err
	}
	patches, err := parseUnifiedDiff(in.Patch)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	if len(patches) == 0 {
		return Result{Content: "no file hunks found in patch", IsError: true}, nil
	}
	var b strings.Builder
	for _, fp := range patches {
		path, err := resolvePath(t.env.Workspace, fp.path)
		if err != nil {
			return Result{Content: err.Error(), IsError: true}, nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return Result{Content: err.Error(), IsError: true}, nil
		}
		updated, err := applyHunks(string(data), fp.hunks)
		if err != nil {
			return Result{Content: fmt.Sprintf("%s: %v", fp.path, err), IsError: true}, nil
		}
		if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
			return Result{Content: err.Error(), IsError: true}, nil
		}
		fmt.Fprintf(&b, "patched %s (%d hunks)\n", fp.path, len(fp.hunks))
	}
	return Result{Content: strings.TrimRight(b.String(), "\n")}, nil
}

func parseUnifiedDiff(diff string) ([]filePatch, error) {
	var files []filePatch
	var current *filePatch
	finalize := func() {
		if current != nil && current.path != "" && current.path != "/dev/null" && len(current.hunks) > 0 {
			files = append(files, *current)
		}
		current = nil
	}
	lines := strings.Split(diff, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		switch {
		case strings.HasPrefix(line, "+++ "):
			path := strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
			path = strings.TrimPrefix(path, "b/")
			if tab := strings.IndexByte(path, '\t'); tab >= 0 {
				path = path[:tab]
			}
			if current != nil {
				current.path = path
			}
		case strings.HasPrefix(line, "--- "):
			finalize()
			current = &filePatch{}
		case strings.HasPrefix(line, "@@"):
			if current == nil {
				return nil, fmt.Errorf("hunk without file header")
			}
			h, err := parseHunkHeader(line)
			if err != nil {
				return nil, err
			}
			// Collect hunk body lines until the next header.
			for i+1 < len(lines) {
				next := lines[i+1]
				if strings.HasPrefix(next, "@@") || strings.HasPrefix(next, "--- ") || strings.HasPrefix(next, "+++ ") || strings.HasPrefix(next, "diff ") {
					break
				}
				i++
				h.lines = append(h.lines, next)
			}
			current.hunks = append(current.hunks, h)
		}
	}
	finalize()
	return files, nil
}

func parseHunkHeader(line string) (patchHunk, error) {
	// @@ -oldStart,oldCount +newStart,newCount @@ optional
	rest := strings.TrimPrefix(line, "@@")
	end := strings.Index(rest, "@@")
	if end >= 0 {
		rest = rest[:end]
	}
	fields := strings.Fields(rest)
	var h patchHunk
	for _, f := range fields {
		if strings.HasPrefix(f, "-") {
			start, _, _ := parseRange(strings.TrimPrefix(f, "-"))
			h.oldStart = start
		}
	}
	if h.oldStart == 0 {
		h.oldStart = 1
	}
	return h, nil
}

func parseRange(s string) (start, count int, err error) {
	parts := strings.SplitN(s, ",", 2)
	start, err = strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, err
	}
	count = 1
	if len(parts) == 2 {
		count, _ = strconv.Atoi(parts[1])
	}
	return start, count, nil
}

func applyHunks(content string, hunks []patchHunk) (string, error) {
	out := strings.Split(content, "\n")
	delta := 0
	for _, h := range hunks {
		var oldLines, newLines []string
		for _, l := range h.lines {
			if l == "" {
				continue
			}
			switch l[0] {
			case ' ':
				oldLines = append(oldLines, l[1:])
				newLines = append(newLines, l[1:])
			case '-':
				oldLines = append(oldLines, l[1:])
			case '+':
				newLines = append(newLines, l[1:])
			case '\\':
				// "\ No newline at end of file" — ignore.
			}
		}
		pos := h.oldStart - 1 + delta
		idx := findBlock(out, oldLines, pos)
		if idx < 0 {
			return "", fmt.Errorf("hunk at line %d does not match file content", h.oldStart)
		}
		replaced := make([]string, 0, len(out)-len(oldLines)+len(newLines))
		replaced = append(replaced, out[:idx]...)
		replaced = append(replaced, newLines...)
		replaced = append(replaced, out[idx+len(oldLines):]...)
		out = replaced
		delta += len(newLines) - len(oldLines)
	}
	return strings.Join(out, "\n"), nil
}

func findBlock(lines, block []string, pos int) int {
	if len(block) == 0 {
		if pos < 0 {
			return 0
		}
		if pos > len(lines) {
			return len(lines)
		}
		return pos
	}
	match := func(i int) bool {
		if i < 0 || i+len(block) > len(lines) {
			return false
		}
		for j := range block {
			if lines[i+j] != block[j] {
				return false
			}
		}
		return true
	}
	if match(pos) {
		return pos
	}
	for offset := 1; offset <= len(lines); offset++ {
		if match(pos - offset) {
			return pos - offset
		}
		if match(pos + offset) {
			return pos + offset
		}
	}
	return -1
}
