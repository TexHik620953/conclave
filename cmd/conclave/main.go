// Command conclave is a multi-role LLM pipeline orchestrator.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

var (
	flagGlobalDir  string
	flagProjectDir string
	flagNoGlobal   bool
	flagDataDir    string
)

func main() {
	root := &cobra.Command{
		Use:           "conclave",
		Short:         "Multi-role LLM pipeline orchestrator",
		Long:          "Conclave orchestrates editable LLM roles over OpenAI-compatible providers (cloud or local).",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	pf := root.PersistentFlags()
	pf.StringVar(&flagGlobalDir, "global-dir", "", "global config directory (default ~/.config/conclave)")
	pf.StringVar(&flagProjectDir, "project-dir", "", "project config directory (default .conclave)")
	pf.BoolVar(&flagNoGlobal, "no-global", false, "ignore the global config directory")
	pf.StringVar(&flagDataDir, "data-dir", "", "directory for the database and artifacts (default .conclave)")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	root.SetContext(ctx)

	root.AddCommand(
		newServeCmd(),
		newRolesCmd(),
		newPipelinesCmd(),
		newRunsCmd(),
		newMCPCmd(),
		newConfigCmd(),
		newVersionCmd(),
	)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
