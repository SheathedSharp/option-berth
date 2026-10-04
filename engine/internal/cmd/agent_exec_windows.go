//go:build windows

package cmd

import (
	"fmt"
	"github.com/sheathedsharp/option-berth/internal/agentlaunch"
)

func executeAgent(agentlaunch.Plan) error {
	return fmt.Errorf("native agent handoff currently requires macOS or Linux; agent plan/list remain available")
}
