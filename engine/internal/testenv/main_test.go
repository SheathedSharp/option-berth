package testenv

import (
	"fmt"
	"io"
	"os"
	"testing"
	"time"
)

// helperEnv turns this binary into a stand-in child process rather than a test
// run. The leak-gate cases need a process that looks to `ps` exactly like a
// leaked daemon — argv[0] a binary, argv[1] "serve" — and the TMPDIR case needs
// a child that reports the temp directory it inherited.
const helperEnv = "BERTH_TESTENV_HELPER"

// The two stand-ins this binary can play.
const (
	// helperTempDir prints the temp directory it inherited and exits.
	helperTempDir = "tmpdir"
	// helperServe sits still, looking like a daemon, until the parent closes its
	// stdin. It never outlives the suite: a stand-in must not become the leak it
	// is imitating.
	helperServe = "serve"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(helperEnv); mode != "" {
		runHelper(mode)
		return
	}
	os.Exit(Run(m))
}

// runHelper is the stand-in process. It never returns.
func runHelper(mode string) {
	switch mode {
	case helperTempDir:
		fmt.Print(os.TempDir())
	case helperServe:
		time.AfterFunc(2*time.Minute, func() { os.Exit(0) })
		_, _ = io.Copy(io.Discard, os.Stdin)
	}
	os.Exit(0)
}
