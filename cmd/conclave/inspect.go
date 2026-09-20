package main

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func newRolesCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "roles", Short: "Inspect configured roles"}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List roles",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			ids := make([]string, 0, len(cfg.Roles))
			for id := range cfg.Roles {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			for _, id := range ids {
				r := cfg.Roles[id]
				tools := "all"
				if len(r.Tools) > 0 {
					tools = fmt.Sprintf("%v", r.Tools)
				}
				fmt.Printf("%-18s %-28s model=%-28s tools=%s\n", id, r.Title, r.Model, tools)
			}
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "show <role>",
		Short: "Show a role definition",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			r, ok := cfg.Roles[args[0]]
			if !ok {
				return fmt.Errorf("unknown role %q", args[0])
			}
			return printYAML(r)
		},
	})
	return cmd
}

func newPipelinesCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "pipelines", Short: "Inspect configured pipelines"}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List pipelines",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			names := make([]string, 0, len(cfg.Pipelines))
			for n := range cfg.Pipelines {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				p := cfg.Pipelines[n]
				fmt.Printf("%-18s nodes=%d start=%s  %s\n", n, len(p.Nodes), p.Start, p.Description)
			}
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "show <pipeline>",
		Short: "Show a pipeline definition",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			p, ok := cfg.Pipelines[args[0]]
			if !ok {
				return fmt.Errorf("unknown pipeline %q", args[0])
			}
			return printYAML(p)
		},
	})
	return cmd
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("conclave 0.1.0")
			return nil
		},
	}
}

func printYAML(v any) error {
	data, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	fmt.Print(string(data))
	return nil
}
