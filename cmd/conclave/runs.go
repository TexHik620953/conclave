package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func newRunsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "runs", Short: "Inspect past runs"}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List recent runs",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, err := loadApp(ctx, false)
			if err != nil {
				return err
			}
			defer a.Close()
			runs, err := a.store.ListRuns(30)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(w, "RUN ID\tPIPELINE\tSTATUS\tSTARTED\tTASK")
			for _, r := range runs {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.ID, r.Pipeline, r.Status,
					r.StartedAt.Format("2006-01-02 15:04:05"), truncateStr(r.Task, 50))
			}
			return w.Flush()
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "show <run-id>",
		Short: "Show a run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, err := loadApp(ctx, false)
			if err != nil {
				return err
			}
			defer a.Close()
			run, err := a.store.GetRun(args[0])
			if err != nil {
				return err
			}
			fmt.Printf("run:       %s\npipeline:  %s\nstatus:    %s\nworkspace: %s\n", run.ID, run.Pipeline, run.Status, run.Workspace)
			if run.CostUSD > 0 {
				fmt.Printf("cost:      $%.6f\n", run.CostUSD)
			}
			if run.PromptTokens > 0 || run.CompletionTokens > 0 {
				fmt.Printf("tokens:    prompt=%d completion=%d\n", run.PromptTokens, run.CompletionTokens)
			}
			if run.Task != "" {
				fmt.Printf("task:      %s\n", run.Task)
			}
			if run.Error != "" {
				fmt.Printf("error:     %s\n", run.Error)
			}
			nodes, err := a.store.ListNodes(args[0])
			if err != nil {
				return err
			}
			fmt.Println("\nnodes:")
			w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(w, "  NODE\tROLE\tSTATUS\tTOKENS")
			total := 0
			for _, n := range nodes {
				fmt.Fprintf(w, "  %s\t%s\t%s\t%d\n", n.NodeID, n.Role, n.Status, n.Tokens)
				total += n.Tokens
			}
			_ = w.Flush()
			fmt.Printf("  total tokens: %d\n", total)
			arts, err := a.store.ListArtifacts(args[0])
			if err != nil {
				return err
			}
			fmt.Println("\nartifacts:")
			for _, art := range arts {
				fmt.Printf("  %s -> %s\n", art.Name, art.Path)
			}
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "artifacts <run-id>",
		Short: "List artifact paths for a run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, err := loadApp(ctx, false)
			if err != nil {
				return err
			}
			defer a.Close()
			arts, err := a.store.ListArtifacts(args[0])
			if err != nil {
				return err
			}
			for _, art := range arts {
				fmt.Println(art.Path)
			}
			return nil
		},
	})

	var limit int
	var filter string
	logsCmd := &cobra.Command{
		Use:   "logs <run-id>",
		Short: "Print the stored event log for a run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, err := loadApp(ctx, false)
			if err != nil {
				return err
			}
			defer a.Close()
			events, err := a.store.ListEvents(args[0], limit)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(w, "TIME\tTYPE\tNODE\tROLE\tMESSAGE")
			for _, ev := range events {
				if filter != "" && ev.Type != filter {
					continue
				}
				msg := strings.ReplaceAll(ev.Message, "\n", " ")
				if len(msg) > 80 {
					msg = msg[:80] + "..."
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
					ev.Time.Format("15:04:05"), ev.Type, ev.NodeID, ev.Role, msg)
			}
			return w.Flush()
		},
	}
	logsCmd.Flags().IntVar(&limit, "limit", 1000, "maximum number of events")
	logsCmd.Flags().StringVar(&filter, "type", "", "only show events of this type")
	cmd.AddCommand(logsCmd)
	return cmd
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
