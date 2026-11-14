package cmd

import (
	"os"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/spf13/cobra"
)

// daemonCmd is the group for daemon inspection and control. The subcommands
// that talk to a running daemon live in daemon_ctl.go.
var daemonCmd = &cobra.Command{
	Use:     "daemon",
	Short:   "Inspect and control the oberth daemon",
	GroupID: commandGroupInfra,
}

// daemonSchemaLong is the schema command's long help, one entry per output line.
var daemonSchemaLong = strings.Join([]string{
	"Print the JSON Schema describing every daemon type and method.",
	"It is generated from the Go types and is byte-identical to the copy",
	"checked in at docs/schema/protocol.schema.json.",
}, "\n")

var daemonSchemaCmd = &cobra.Command{
	Use:   "schema",
	Short: "Print the daemon protocol JSON Schema",
	Long:  daemonSchemaLong,
	Args:  cobra.NoArgs,
	RunE:  runDaemonSchema,
}

// runDaemonSchema writes the generated schema to stdout, where it is the whole
// answer.
func runDaemonSchema(_ *cobra.Command, _ []string) error {
	schema := rpc.Marshal()
	_, err := os.Stdout.Write(schema)
	return err
}

func init() {
	daemonCmd.AddCommand(daemonSchemaCmd)
	rootCmd.AddCommand(daemonCmd)
}
