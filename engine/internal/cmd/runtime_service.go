package cmd

import (
	"os"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/spf13/cobra"
)

// runtimeServiceCmd is the group for runtime service inspection and control. The subcommands
// that talk to a running runtime service live in runtime_service_ctl.go.
var runtimeCmd = &cobra.Command{
	Use:     "runtime",
	Short:   "Inspect runtime facts and manage their Runtime Service host",
	GroupID: commandGroupInfra,
}

var runtimeServiceCmd = &cobra.Command{
	Use:   "service",
	Short: "Inspect and control the oberth runtime service",
}

// runtimeServiceSchemaLong is the schema command's long help, one entry per output line.
var runtimeServiceSchemaLong = strings.Join([]string{
	"Print the JSON Schema describing every runtime service type and method.",
	"It is generated from the Go types and is byte-identical to the copy",
	"checked in at docs/schema/protocol.schema.json.",
}, "\n")

var runtimeServiceSchemaCmd = &cobra.Command{
	Use:   "schema",
	Short: "Print the runtime service protocol JSON Schema",
	Long:  runtimeServiceSchemaLong,
	Args:  cobra.NoArgs,
	RunE:  runRuntimeServiceSchema,
}

// runRuntimeServiceSchema writes the generated schema to stdout, where it is the whole
// answer.
func runRuntimeServiceSchema(_ *cobra.Command, _ []string) error {
	schema := rpc.Marshal()
	_, err := os.Stdout.Write(schema)
	return err
}

func init() {
	runtimeServiceCmd.AddCommand(runtimeServiceSchemaCmd)
	runtimeCmd.AddCommand(runtimeServiceCmd)
	rootCmd.AddCommand(runtimeCmd)
}
