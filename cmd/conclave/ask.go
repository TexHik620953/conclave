package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/texhik/conclave/internal/provider"
	"github.com/texhik/conclave/internal/role"
)

func newAskCmd() *cobra.Command {
	var prompt, workspace, contextText string
	var stream bool
	cmd := &cobra.Command{
		Use:   "ask <role>",
		Short: "Ask a single role a one-off question",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, err := loadApp(ctx, true)
			if err != nil {
				return err
			}
			defer a.Close()

			rt, err := a.buildRoleRuntime(ctx, args[0], workspace)
			if err != nil {
				return err
			}
			if prompt == "" && contextText == "" {
				return fmt.Errorf("provide --prompt and/or --context")
			}
			if stream {
				rt.OnDelta = func(d provider.Delta) {
					if d.Content != "" {
						fmt.Fprint(os.Stdout, d.Content)
					}
				}
			}
			res, err := rt.Run(ctx, role.Input{Prompt: prompt, Context: contextText})
			if err != nil {
				return err
			}
			if stream {
				fmt.Fprintln(os.Stdout)
			} else {
				fmt.Fprintln(os.Stdout, res.Content)
			}
			fmt.Fprintf(os.Stderr, "\n[role=%s model=%s/%s tokens=%d tools=%d]\n",
				args[0], res.Provider, res.Model, res.Usage.TotalTokens, res.ToolCalls)
			return nil
		},
	}
	cmd.Flags().StringVar(&prompt, "prompt", "", "prompt to send")
	cmd.Flags().StringVar(&contextText, "context", "", "additional context")
	cmd.Flags().StringVar(&workspace, "workspace", "", "workspace directory")
	cmd.Flags().BoolVar(&stream, "stream", true, "stream the response to stdout")
	return cmd
}
