package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/texhik/conclave/internal/mcp"
	"github.com/texhik/conclave/internal/orchestrator"
	"github.com/texhik/conclave/internal/role"
	"github.com/texhik/conclave/internal/tool"
)

func newMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Serve conclave as an MCP server over stdio",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, err := loadApp(ctx, true)
			if err != nil {
				return err
			}
			defer a.Close()
			for _, e := range a.mcpErrors() {
				fmt.Fprintln(os.Stderr, "warning:", e)
			}
			srv := &mcp.Server{Name: "conclave", Version: version, In: os.Stdin, Out: os.Stdout}
			a.engine.Asker = &elicitationAsker{srv: srv}
			registerMCPTools(srv, a)
			return srv.Serve(ctx)
		},
	}
}

// elicitationAsker delivers ask_user questions to the MCP client via
// elicitation/create, falling back to non-interactive behaviour when the
// client does not support it.
type elicitationAsker struct {
	srv *mcp.Server
}

func (a *elicitationAsker) Ask(ctx context.Context, q tool.Question) (tool.Answer, error) {
	ectx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	res, err := a.srv.Elicit(ectx, q.Question, questionSchema(q))
	if err != nil {
		return tool.Answer{}, tool.ErrNoInteractiveUser
	}
	if res.Action != "accept" {
		return tool.Answer{}, nil
	}
	return answerFromContent(res.Content, q), nil
}

func questionSchema(q tool.Question) json.RawMessage {
	props := map[string]any{}
	var required []string
	labels := make([]string, 0, len(q.Options))
	for _, o := range q.Options {
		labels = append(labels, o.Label)
	}
	if len(labels) > 0 {
		if q.Multiple {
			props["selected"] = map[string]any{
				"type": "array", "title": "Selection",
				"items": map[string]any{"type": "string", "enum": labels},
			}
		} else {
			props["selected"] = map[string]any{"type": "string", "enum": labels, "title": "Selection"}
		}
		required = append(required, "selected")
	}
	if q.AllowCustom || len(labels) == 0 {
		props["custom"] = map[string]any{"type": "string", "title": "Your answer"}
	}
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	b, _ := json.Marshal(s)
	return b
}

func answerFromContent(content map[string]any, q tool.Question) tool.Answer {
	var ans tool.Answer
	if v, ok := content["selected"]; ok {
		switch t := v.(type) {
		case string:
			if t != "" {
				ans.Selected = []string{t}
			}
		case []any:
			for _, x := range t {
				if s, ok := x.(string); ok {
					ans.Selected = append(ans.Selected, s)
				}
			}
		case []string:
			ans.Selected = t
		}
	}
	if v, ok := content["custom"].(string); ok {
		ans.Custom = v
	}
	return ans
}

func registerMCPTools(srv *mcp.Server, a *app) {
	srv.AddTool(mcp.ToolSpec{
		Name:        "list_roles",
		Description: "List the configured LLM roles.",
		Schema:      objSchema(map[string]any{}),
		Handler: func(ctx context.Context, args json.RawMessage) (*mcp.CallToolResult, error) {
			ids := make([]string, 0, len(a.cfg.Roles))
			for id := range a.cfg.Roles {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			var b strings.Builder
			for _, id := range ids {
				r := a.cfg.Roles[id]
				fmt.Fprintf(&b, "- %s: %s (model=%s)\n", id, r.Title, r.Model)
			}
			return mcp.Text(b.String()), nil
		},
	})

	srv.AddTool(mcp.ToolSpec{
		Name:        "list_pipelines",
		Description: "List the configured pipelines.",
		Schema:      objSchema(map[string]any{}),
		Handler: func(ctx context.Context, args json.RawMessage) (*mcp.CallToolResult, error) {
			names := make([]string, 0, len(a.cfg.Pipelines))
			for n := range a.cfg.Pipelines {
				names = append(names, n)
			}
			sort.Strings(names)
			var b strings.Builder
			for _, n := range names {
				p := a.cfg.Pipelines[n]
				fmt.Fprintf(&b, "- %s: %s (nodes=%d)\n", n, p.Description, len(p.Nodes))
			}
			return mcp.Text(b.String()), nil
		},
	})

	srv.AddTool(mcp.ToolSpec{
		Name:        "run_pipeline",
		Description: "Run a task with the multi-role team. The controller decides which roles to run; a pipeline is optional and defaults to 'auto'.",
		Schema: objSchema(map[string]any{
			"pipeline":  map[string]any{"type": "string", "description": "Optional pipeline name; defaults to the controller-driven 'auto'."},
			"task":      map[string]any{"type": "string", "description": "Task description."},
			"workspace": map[string]any{"type": "string", "description": "Workspace directory."},
			"inputs":    map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
		}, "task"),
		Handler: func(ctx context.Context, args json.RawMessage) (*mcp.CallToolResult, error) {
			var in struct {
				Pipeline  string            `json:"pipeline"`
				Task      string            `json:"task"`
				Workspace string            `json:"workspace"`
				Inputs    map[string]string `json:"inputs"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return mcp.ErrorText(err.Error()), nil
			}
			res, err := a.engine.Run(ctx, orchestrator.RunOptions{
				Pipeline: in.Pipeline, Task: in.Task, Workspace: in.Workspace, Inputs: in.Inputs,
			})
			if res == nil {
				return mcp.ErrorText(err.Error()), nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "run_id: %s\nstatus: %s\n", res.RunID, res.Status)
			if res.Error != "" {
				fmt.Fprintf(&b, "error: %s\n", res.Error)
			}
			names := make([]string, 0, len(res.Artifacts))
			for name := range res.Artifacts {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				fmt.Fprintf(&b, "\n===== artifact: %s =====\n%s\n", name, clip(res.Artifacts[name], 8000))
			}
			out := mcp.Text(b.String())
			out.IsError = err != nil
			return out, nil
		},
	})

	srv.AddTool(mcp.ToolSpec{
		Name:        "ask_role",
		Description: "Ask a single role a question and return its answer.",
		Schema: objSchema(map[string]any{
			"role":      map[string]any{"type": "string"},
			"prompt":    map[string]any{"type": "string"},
			"context":   map[string]any{"type": "string"},
			"workspace": map[string]any{"type": "string"},
		}, "role", "prompt"),
		Handler: func(ctx context.Context, args json.RawMessage) (*mcp.CallToolResult, error) {
			var in struct {
				Role      string `json:"role"`
				Prompt    string `json:"prompt"`
				Context   string `json:"context"`
				Workspace string `json:"workspace"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return mcp.ErrorText(err.Error()), nil
			}
			rt, err := a.buildRoleRuntime(ctx, in.Role, in.Workspace)
			if err != nil {
				return mcp.ErrorText(err.Error()), nil
			}
			res, err := rt.Run(ctx, role.Input{Prompt: in.Prompt, Context: in.Context})
			if err != nil {
				return mcp.ErrorText(err.Error()), nil
			}
			return mcp.Text(res.Content), nil
		},
	})

	srv.AddTool(mcp.ToolSpec{
		Name:        "get_run",
		Description: "Return the status and node list for a run.",
		Schema: objSchema(map[string]any{
			"run_id": map[string]any{"type": "string"},
		}, "run_id"),
		Handler: func(ctx context.Context, args json.RawMessage) (*mcp.CallToolResult, error) {
			var in struct {
				RunID string `json:"run_id"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return mcp.ErrorText(err.Error()), nil
			}
			run, err := a.store.GetRun(in.RunID)
			if err != nil {
				return mcp.ErrorText(err.Error()), nil
			}
			nodes, err := a.store.ListNodes(in.RunID)
			if err != nil {
				return mcp.ErrorText(err.Error()), nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "run: %s\npipeline: %s\nstatus: %s\nerror: %s\n", run.ID, run.Pipeline, run.Status, run.Error)
			for _, n := range nodes {
				fmt.Fprintf(&b, "- %s [%s] role=%s tokens=%d\n", n.NodeID, n.Status, n.Role, n.Tokens)
			}
			return mcp.Text(b.String()), nil
		},
	})

	srv.AddTool(mcp.ToolSpec{
		Name:        "get_artifact",
		Description: "Return the content of a named artifact from a run.",
		Schema: objSchema(map[string]any{
			"run_id": map[string]any{"type": "string"},
			"name":   map[string]any{"type": "string"},
		}, "run_id", "name"),
		Handler: func(ctx context.Context, args json.RawMessage) (*mcp.CallToolResult, error) {
			var in struct {
				RunID string `json:"run_id"`
				Name  string `json:"name"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return mcp.ErrorText(err.Error()), nil
			}
			arts, err := a.store.ListArtifacts(in.RunID)
			if err != nil {
				return mcp.ErrorText(err.Error()), nil
			}
			for _, art := range arts {
				if art.Name == in.Name {
					data, err := os.ReadFile(art.Path)
					if err != nil {
						return mcp.ErrorText(err.Error()), nil
					}
					return mcp.Text(string(data)), nil
				}
			}
			return mcp.ErrorText(fmt.Sprintf("artifact %q not found in run %s", in.Name, in.RunID)), nil
		},
	})
}

func objSchema(props map[string]any, required ...string) json.RawMessage {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	b, _ := json.Marshal(s)
	return b
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n... [truncated]"
}
