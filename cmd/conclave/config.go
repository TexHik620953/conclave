package main

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/texhik/conclave/internal/config"
)

//go:embed seed
var seedFS embed.FS

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "Manage configuration"}
	var global, force bool
	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Write seed configuration (providers, roles, prompts, pipelines)",
		RunE: func(cmd *cobra.Command, args []string) error {
			target := flagProjectDir
			if target == "" {
				target = config.DefaultProjectDir()
			}
			if global {
				target = flagGlobalDir
				if target == "" {
					target = config.DefaultGlobalDir()
				}
			}
			return writeSeed(target, force)
		},
	}
	initCmd.Flags().BoolVar(&global, "global", false, "write to the global config directory instead of the project")
	initCmd.Flags().BoolVar(&force, "force", false, "overwrite existing files")

	validateCmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate the merged configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			fmt.Printf("ok: %d providers, %d roles, %d pipelines\n",
				len(cfg.Providers), len(cfg.Roles), len(cfg.Pipelines))
			return nil
		},
	}
	showCmd := &cobra.Command{
		Use:   "show",
		Short: "Print the config directory in use",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			fmt.Printf("base dir: %s\n", cfg.BaseDir)
			return nil
		},
	}
	cmd.AddCommand(initCmd, validateCmd, showCmd)
	cmd.AddCommand(&cobra.Command{
		Use:   "schema",
		Short: "Print the configuration JSON schema",
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := config.Schema()
			if err != nil {
				return err
			}
			fmt.Println(string(data))
			return nil
		},
	})
	return cmd
}

func writeSeed(target string, force bool) error {
	count := 0
	err := fs.WalkDir(seedFS, "seed", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel("seed", path)
		if err != nil {
			return err
		}
		dest := filepath.Join(target, rel)
		if _, statErr := os.Stat(dest); statErr == nil && !force {
			return fmt.Errorf("%s already exists (use --force)", dest)
		}
		data, err := seedFS.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dest, data, 0o644); err != nil {
			return err
		}
		count++
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Printf("wrote %d files to %s\n", count, target)
	return nil
}
