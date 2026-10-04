package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fixtureLog(t *testing.T) *os.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.log")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}
func TestDaemonLogsPropagateClosedOutput(t *testing.T) {
	for _, follow := range []bool{false, true} {
		t.Run(map[bool]string{false: "tail", true: "follow"}[follow], func(t *testing.T) {
			f := fixtureLog(t)
			out, err := os.CreateTemp(t.TempDir(), "closed")
			if err != nil {
				t.Fatal(err)
			}
			out.Close()
			original := os.Stdout
			os.Stdout = out
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			if follow {
				err = followFile(ctx, f)
			} else {
				err = printTail(f, 2)
			}
			cancel()
			os.Stdout = original
			if !errors.Is(err, os.ErrClosed) {
				t.Fatal("output failure was reported as successful log delivery")
			}
		})
	}
}

type shortLogWriter struct{}

func (shortLogWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }
func TestDaemonLogShortWriteAndCancellation(t *testing.T) {
	if err := printTailTo(fixtureLog(t), 2, shortLogWriter{}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal(err)
	}
	if err := followFileTo(context.Background(), fixtureLog(t), shortLogWriter{}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	if err := followFileTo(ctx, fixtureLog(t), &out); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatal("wrote output after pre-cancellation")
	}
}
func TestDaemonLogTailOrdering(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want string
	}{{0, ""}, {1, "three\n"}, {2, "two\nthree\n"}, {10, "one\ntwo\nthree\n"}} {
		var out bytes.Buffer
		if err := printTailTo(fixtureLog(t), tc.n, &out); err != nil {
			t.Fatal(err)
		}
		if out.String() != tc.want {
			t.Fatalf("tail %d=%q want=%q", tc.n, out.String(), tc.want)
		}
	}
}
