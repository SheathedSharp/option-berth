//go:build !windows

package usersvc

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// run invokes one supervisor command and reports its own failure verbatim.
//
// The command is resolved through PATH and the arguments are passed as given:
// no shell, so a path with a space, or a label that looks like an option, cannot
// turn into two arguments or into a second command.
func run(ctx context.Context, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	command.Stdout = io.Discard
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		call := name + " " + strings.Join(args, " ")
		if detail := firstLine(stderr.String()); detail != "" {
			return fmt.Errorf("%s: %w: %s", call, err, detail)
		}
		return fmt.Errorf("%s: %w", call, err)
	}
	return nil
}

// firstLine keeps a supervisor's multi-line complaint from swallowing the error
// it belongs to.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
