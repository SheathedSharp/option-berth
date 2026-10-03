package cmd

import (
	"encoding/json"
	"io"
	"os"
)

// encodeJSON writes one value as indented JSON to w.
//
// Every JSON code path in the CLI goes through here, because docs/cli.md
// promises one shape. This file used to compete with three other spellings of
// the same idea — `writeJSON` in groups.go, a hand-rolled MarshalIndent in
// daemon_ctl.go, and display.RenderJSON's own encoder — which is exactly how
// two commands end up disagreeing about what `--json` prints.
func encodeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	// The default escaping turns `<`, `>` and `&` into \u003c and friends, so a
	// command path or a log line comes back mangled. Agent callers read these
	// strings.
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// printJSON is encodeJSON to stdout: what a command's `--json` prints.
func printJSON(v any) error {
	return encodeJSON(os.Stdout, v)
}

// encodeJSONLine is the compact counterpart used by streaming commands. A
// progress event must occupy exactly one line so a client can render it as it
// arrives; ordinary --json output keeps its indented human-readable shape.
func encodeJSONLine(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// nonNilStrings keeps "no lines" an empty array in JSON rather than null: a
// caller should not have to branch on which kind of nothing it got.
func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// nonNilInts is nonNilStrings for port lists.
func nonNilInts(v []int) []int {
	if v == nil {
		return []int{}
	}
	return v
}
