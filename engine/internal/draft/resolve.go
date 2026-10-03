package draft

import (
	"errors"
	"fmt"
	"strings"

	"github.com/sheathedsharp/option-berth/internal/spawn"
)

// The ways resolving an agent CLI fails, so a caller can map the failure onto
// its own vocabulary without reading the message. Every error Resolve returns
// wraps exactly one of these.
var (
	// ErrUnknownAgent — the name is not one of the supported CLIs.
	ErrUnknownAgent = errors.New("unknown agent")
	// ErrNotInstalled — the CLI was named (or found) but does not resolve on
	// PATH. The message is spawn.CheckRunnable's own, so "not on PATH" and
	// "on PATH but cannot be run" stay told apart here too.
	ErrNotInstalled = errors.New("agent not installed")
	// ErrNoAgent and ErrManyAgents — auto-detection did not land on exactly
	// one CLI. Neither is guessed around: a draft command that silently picked
	// one of two would be a coin toss the user never saw.
	ErrNoAgent    = errors.New("no agent found")
	ErrManyAgents = errors.New("several agents found")
)

// ResolveError carries the sentence to show and the sentinel to branch on.
type ResolveError struct {
	Kind error
	Msg  string
}

func (e *ResolveError) Error() string { return e.Msg }
func (e *ResolveError) Unwrap() error { return e.Kind }

// Resolve picks the CLI to run: the caller's name, else the configured one,
// else the single supported CLI on PATH.
//
// name is what the positional agent argument (or the wire's agent parameter) carried; configured is
// config.agent.command. Both may be empty, and an empty result of
// auto-detection is an error — never a guess. The CLI entry point uses this
// one resolver so an explicit choice and auto-detection have one contract.
func Resolve(name, configured string) (CLI, error) {
	want := strings.TrimSpace(name)
	if want == "" {
		want = strings.TrimSpace(configured)
	}
	if want != "" {
		cli, ok := Lookup(want)
		if !ok {
			return CLI{}, &ResolveError{
				Kind: ErrUnknownAgent,
				Msg:  fmt.Sprintf("unknown agent %q — supported: %s", want, strings.Join(Supported(), ", ")),
			}
		}
		if err := spawn.CheckRunnable(cli.Bin, nil); err != nil {
			return CLI{}, &ResolveError{Kind: ErrNotInstalled, Msg: err.Error()}
		}
		return cli, nil
	}

	found := Detect(nil)
	switch len(found) {
	case 0:
		return CLI{}, &ResolveError{
			Kind: ErrNoAgent,
			Msg:  "no supported agent CLI on PATH (looked for " + strings.Join(Bins(), ", ") + ")",
		}
	case 1:
		return found[0], nil
	default:
		names := make([]string, 0, len(found))
		for _, c := range found {
			names = append(names, c.Name)
		}
		return CLI{}, &ResolveError{
			Kind: ErrManyAgents,
			Msg:  "several agent CLIs are on PATH (" + strings.Join(names, ", ") + ") — none was chosen",
		}
	}
}
