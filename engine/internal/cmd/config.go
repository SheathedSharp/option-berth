package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"github.com/sheathedsharp/option-berth/engine/internal/config"
	"github.com/spf13/cobra"
)

var (
	configForce    bool
	configJSONFlag bool
)

// newConfigCommand builds the `oberth config` group. The commands are created
// here rather than as package-level vars so the flag wiring stays in one
// place.
func newConfigCommand() *cobra.Command {
	group := &cobra.Command{
		Use:     "config",
		Short:   "Manage the oberth config file",
		GroupID: commandGroupInfra,
	}
	pathCommand := &cobra.Command{
		Use:   "path",
		Short: "Print the config file path",
		RunE:  runConfigPath,
	}
	initCommand := &cobra.Command{
		Use:   "init",
		Short: "Write a starter config file",
		RunE:  runConfigInitCommand,
	}
	editCommand := &cobra.Command{
		Use:   "edit",
		Short: "Open the config file in $EDITOR",
		RunE: func(*cobra.Command, []string) error {
			return runConfigEdit()
		},
	}

	initCommand.Flags().BoolVar(&configForce, "force", false, "Overwrite an existing config file")
	pathCommand.Flags().BoolVar(&configJSONFlag, "json", false, "Output as JSON")
	initCommand.Flags().BoolVar(&configJSONFlag, "json", false, "Output as JSON")
	group.AddCommand(pathCommand, initCommand, editCommand)
	return group
}

var configCmd = newConfigCommand()

func init() {
	rootCmd.AddCommand(configCmd)
}

func runConfigPath(*cobra.Command, []string) error {
	if configJSONFlag {
		return printJSON(map[string]any{"path": config.Path()})
	}
	fmt.Println(config.Path())
	return nil
}

func runConfigInitCommand(*cobra.Command, []string) error {
	if err := runConfigInit(configForce); err != nil {
		return err
	}
	if configJSONFlag {
		return printJSON(map[string]any{"path": config.Path(), "written": true})
	}
	fmt.Printf("wrote %s\n", config.Path())
	return nil
}

func runConfigInit(force bool) error {
	return config.WriteTemplate(force)
}

func runConfigEdit() error {
	path := config.Path()
	editor := resolveEditor()
	if editor == "" {
		return fmt.Errorf("no editor found: set $EDITOR (or $VISUAL)")
	}
	// The editor should open a real file, so write the starter when the file
	// does not exist yet. Resolving the editor first keeps a machine with no
	// editor from gaining a config file it cannot open.
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := config.WriteTemplate(false); err != nil {
			return err
		}
	}
	command := exec.Command(editor, path)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	return command.Run()
}

func resolveEditor() string {
	for _, key := range []string{"EDITOR", "VISUAL"} {
		if value := os.Getenv(key); value != "" {
			return value
		}
	}
	if runtime.GOOS == "windows" {
		return "notepad"
	}
	if _, err := exec.LookPath("vi"); err == nil {
		return "vi"
	}
	return ""
}
