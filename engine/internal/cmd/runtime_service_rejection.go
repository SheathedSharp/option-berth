package cmd

import (
	"fmt"
	"strings"
)

// This is a refusal, not a Cobra alias or a route to the new service. Stop
// parsing at -- so a user's service arguments cannot become product flags.
func rejectRetiredRuntimeCommand(args []string) error {
	firstCommand := ""
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if arg == "--no-daemon" || strings.HasPrefix(arg, "--no-daemon=") {
			return fmt.Errorf("--no-daemon was removed; use --no-runtime-service")
		}
		if firstCommand == "" && !strings.HasPrefix(arg, "-") {
			firstCommand = arg
		}
	}
	switch firstCommand {
	case "daemon":
		return fmt.Errorf("the daemon command was removed; use oberth runtime service; old hosts must be stopped explicitly with their own matching CLI before switching")
	case "serve":
		return fmt.Errorf("the serve command was removed; use oberth runtime service run [--detach]")
	}
	return nil
}
