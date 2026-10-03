package cmd

import (
	"fmt"
	"runtime"

	"github.com/sheathedsharp/option-berth/internal/buildinfo"
	"github.com/sheathedsharp/option-berth/internal/display"
	"github.com/spf13/cobra"
)

type versionDocument struct {
	Version  string `json:"version"`
	Commit   string `json:"commit"`
	Built    string `json:"built"`
	Platform string `json:"platform"`
}

var versionJSONFlag bool

var versionCmd = &cobra.Command{
	Use:     "version",
	Short:   "Print the version of option-berth",
	GroupID: commandGroupInfra,
	RunE:    versionRun,
}

func init() {
	versionCmd.Flags().BoolVar(&versionJSONFlag, "json", false, "Output as JSON")
	rootCmd.AddCommand(versionCmd)
}

// versionRun prints what this build is. There is deliberately no `--check`:
// option-berth is built from source and has no published release feed to compare
// itself against, and nothing here may go looking for a newer binary to
// install over this one.
func versionRun(cmd *cobra.Command, args []string) error {
	if versionJSONFlag {
		return printJSON(versionDocument{
			Version:  buildinfo.Version,
			Commit:   buildinfo.Commit,
			Built:    buildinfo.Date,
			Platform: runtime.GOOS + "/" + runtime.GOARCH,
		})
	}
	fmt.Println(buildinfo.VersionString())
	if buildinfo.Commit != "unknown" {
		fmt.Println(display.Dim(fmt.Sprintf("commit %s, built %s", buildinfo.Commit, buildinfo.Date)))
	}
	return nil
}
