package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/buildinfo"
	"github.com/sheathedsharp/option-berth/engine/internal/display"
	"github.com/sheathedsharp/option-berth/engine/internal/versioncheck"
	"github.com/spf13/cobra"
)

type versionDocument struct {
	Version  string `json:"version"`
	Commit   string `json:"commit"`
	Built    string `json:"built"`
	Platform string `json:"platform"`
	// Latest is present only for `version --check`: the plain document stays
	// the small, offline shape install scripts and clients already read.
	Latest *latestDocument `json:"latest,omitempty"`
}

type latestDocument struct {
	Version         string `json:"version"`
	CheckedAt       string `json:"checked_at"`
	UpdateAvailable bool   `json:"update_available"`
}

var (
	versionJSONFlag  bool
	versionCheckFlag bool
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version of option-berth",
	Long: "Print the version of option-berth.\n\n" +
		"With --check, also read the latest published release. The check only reads the release\n" +
		"tag; it never downloads or installs a binary.",
	GroupID: commandGroupInfra,
	RunE:    versionRun,
}

func init() {
	versionCmd.Flags().BoolVar(&versionJSONFlag, "json", false, "Output as JSON")
	versionCmd.Flags().BoolVar(&versionCheckFlag, "check", false, "Check the latest released version")
	rootCmd.AddCommand(versionCmd)
}

// versionRun prints what this build is. `--check` adds a read-only comparison
// with the latest published release; nothing here replaces the running binary.
func versionRun(cmd *cobra.Command, args []string) error {
	if !versionCheckFlag {
		if versionJSONFlag {
			return printJSON(versionDocumentFor(nil))
		}
		printVersionIdentity()
		return nil
	}

	// The human path answers "what am I running" before it goes to the
	// network; the JSON path still writes exactly one value.
	if !versionJSONFlag {
		printVersionIdentity()
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	current := buildinfo.VersionValue()
	state, err := versioncheck.Lookup(ctx, current, true, versioncheck.CheckTimeout)
	if err != nil {
		code := "internal"
		if errors.Is(err, context.DeadlineExceeded) {
			code = "timeout"
		}
		return fail(code, "could not check the latest release: %s", err)
	}
	if versionJSONFlag {
		return printJSON(versionDocumentFor(&state))
	}
	fmt.Println(latestLine(state))
	if state.UpdateAvailable() {
		printUpgrade(os.Stdout, state.Latest)
	}
	return nil
}

// versionDocumentFor renders the build identity, with the latest-release answer
// when a check produced one.
func versionDocumentFor(state *versioncheck.State) versionDocument {
	document := versionDocument{
		Version:  buildinfo.VersionValue(),
		Commit:   buildinfo.Commit,
		Built:    buildinfo.Date,
		Platform: runtime.GOOS + "/" + runtime.GOARCH,
	}
	if state != nil {
		document.Latest = &latestDocument{
			Version:         state.Latest,
			CheckedAt:       state.CheckedAt.UTC().Format(time.RFC3339),
			UpdateAvailable: state.UpdateAvailable(),
		}
	}
	return document
}

func printVersionIdentity() {
	fmt.Println(buildinfo.VersionString())
	if buildinfo.Commit != "unknown" {
		fmt.Println(display.Dim(fmt.Sprintf("commit %s, built %s", buildinfo.Commit, buildinfo.Date)))
	}
}

// latestLine is the one sentence a person needs from `version --check`: what
// the feed says, and how this build stands against it.
func latestLine(state versioncheck.State) string {
	line := "latest " + display.Bold(state.Latest)
	switch state.Relation() {
	case versioncheck.Same:
		line += " " + display.Green("(up to date)")
	case versioncheck.Behind:
		line += " " + display.Yellow("(update available)")
	case versioncheck.Ahead:
		line += " " + display.Dim("(this build is newer)")
	}
	return line
}
