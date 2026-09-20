package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/texhik/conclave/internal/orchestrator"
	"github.com/texhik/conclave/internal/tool"
)

func newRunCmd() *cobra.Command {
	var task, workspace, resume string
	var inputs []string
	cmd := &cobra.Command{
		Use:   "run [pipeline]",
		Short: "Run a pipeline (or resume a previous run with --resume)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			pipeline := ""
			if len(args) > 0 {
				pipeline = args[0]
			}
			if pipeline == "" && resume == "" {
				return fmt.Errorf("provide a pipeline name or --resume <run-id>")
			}
			a, err := loadApp(ctx, true)
			if err != nil {
				return err
			}
			defer a.Close()
			for _, e := range a.mcpErrors() {
				fmt.Fprintln(os.Stderr, "warning:", e)
			}
			cancel := a.subscribeEvents(ctx)
			defer cancel()

			res, runErr := a.engine.Run(ctx, orchestrator.RunOptions{
				Pipeline:    pipeline,
				Task:        task,
				Workspace:   workspace,
				Inputs:      parseInputs(inputs),
				ResumeRunID: resume,
			})
			if res != nil {
				fmt.Fprintf(os.Stdout, "\nrun %s: %s\n", res.RunID, res.Status)
				fmt.Fprintf(os.Stdout, "artifacts: %s\n", strings.Join(artifactNames(res), ", "))
				fmt.Fprintf(os.Stdout, "tokens: prompt=%d completion=%d total=%d\n",
					res.Usage.PromptTokens, res.Usage.CompletionTokens, res.Usage.TotalTokens)
				if res.CostUSD > 0 {
					fmt.Fprintf(os.Stdout, "cost: $%.6f\n", res.CostUSD)
				}
				if res.Error != "" {
					fmt.Fprintf(os.Stdout, "error: %s\n", res.Error)
				}
				if len(res.Todos) > 0 {
					fmt.Fprintf(os.Stdout, "\ntodos:\n%s\n", tool.RenderTodos(res.Todos))
				}
			}
			return runErr
		},
	}
	cmd.Flags().StringVar(&task, "task", "", "task description passed to the pipeline")
	cmd.Flags().StringVar(&workspace, "workspace", "", "workspace directory (default: current directory)")
	cmd.Flags().StringArrayVar(&inputs, "input", nil, "pipeline input as key=value (repeatable)")
	cmd.Flags().StringVar(&resume, "resume", "", "resume a previous run by id")
	return cmd
}

func parseInputs(pairs []string) map[string]string {
	out := map[string]string{}
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			out[p] = ""
			continue
		}
		out[k] = v
	}
	return out
}

func artifactNames(res *orchestrator.RunResult) []string {
	names := make([]string, 0, len(res.Artifacts))
	for name := range res.Artifacts {
		names = append(names, name)
	}
	if len(names) == 0 {
		return []string{"(none)"}
	}
	return names
}

func (a *app) mcpErrors() []string {
	if a.mcp == nil {
		return nil
	}
	return a.mcp.Errors()
}
