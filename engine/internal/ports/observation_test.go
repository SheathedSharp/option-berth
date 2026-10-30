package ports

import (
	"errors"
	"strings"
	"testing"
)

// A sandboxed darwin netstat is the shape these fixtures come from: the
// command is allowed to start, the sysctl behind the TCP table is denied, and
// netstat still exits 0 with empty stdout and one diagnostic on stderr.
func TestCollectListenersClassifiesEmptySuccessfulOutput(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
	}{
		{name: "sandbox denial", stderr: "netstat: sysctl: net.inet.tcp.pcblist_n: Operation not permitted\n"},
		{name: "silent empty output", stderr: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := func(string, ...string) ([]byte, []byte, error) {
				return nil, []byte(tt.stderr), nil
			}
			rows, err := collectListeners("darwin", run)
			if rows != nil {
				t.Fatalf("rows = %+v, want none", rows)
			}
			var oe *ObservationError
			if !errors.As(err, &oe) {
				t.Fatalf("error = %v, want *ObservationError", err)
			}
			if oe.Code != CodeObservationEmpty {
				t.Fatalf("code = %q, want %q", oe.Code, CodeObservationEmpty)
			}
			if oe.Hint == "" || !strings.Contains(oe.Hint, "listener_observation") {
				t.Fatalf("hint = %q, want the doctor check named", oe.Hint)
			}
		})
	}
}

// One unreadable row discards the whole pass: a caller must not be told a
// listener is absent because the decoder stopped early.
func TestCollectListenersClassifiesMalformedRow(t *testing.T) {
	run := func(string, ...string) ([]byte, []byte, error) {
		out := darwinTCPHeader + "tcp4 0 0 127.0.0.1.8123 *.* LISTEN\n"
		return []byte(out), nil, nil
	}
	rows, err := collectListeners("darwin", run)
	if rows != nil {
		t.Fatalf("rows = %+v, want the incomplete pass discarded", rows)
	}
	var oe *ObservationError
	if !errors.As(err, &oe) {
		t.Fatalf("error = %v, want *ObservationError", err)
	}
	if oe.Code != CodeObservationIncomplete {
		t.Fatalf("code = %q, want %q", oe.Code, CodeObservationIncomplete)
	}
	if !strings.Contains(oe.Error(), "line 2") {
		t.Fatalf("detail = %q, want the offending line number", oe.Error())
	}
	if oe.Hint == "" {
		t.Fatal("incomplete observation has no hint")
	}
}

func TestCollectListenersKeepsAValidTable(t *testing.T) {
	run := func(string, ...string) ([]byte, []byte, error) {
		out := darwinTCPHeader + "tcp4 0 0 127.0.0.1.8123 *.* LISTEN 0 0 131072 131072 node:42 0 0\n"
		return []byte(out), nil, nil
	}
	rows, err := collectListeners("darwin", run)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %+v, error = %v", rows, err)
	}
}

func TestDecodeDarwinEmptyTableNamesTheObservationCode(t *testing.T) {
	for _, text := range []string{"", "permission denied\n"} {
		var oe *ObservationError
		if !errors.As(mustDecodeDarwin(t, text), &oe) || oe.Code != CodeObservationEmpty {
			t.Fatalf("input %q did not classify as %q: %v", text, CodeObservationEmpty, mustDecodeDarwin(t, text))
		}
	}
}

func mustDecodeDarwin(t *testing.T, text string) error {
	t.Helper()
	_, err := decodeDarwinNetstat(text)
	return err
}
