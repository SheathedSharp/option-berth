package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/client"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
)

// fakeStartStream is the read half of groups.start without a daemon.
type fakeStartStream struct {
	chunks chan json.RawMessage
	end    chan client.End
}

func newFakeStartStream(chunks []rpc.GroupsStartChunk, end client.End) *fakeStartStream {
	f := &fakeStartStream{chunks: make(chan json.RawMessage, len(chunks)), end: make(chan client.End, 1)}
	for _, c := range chunks {
		raw, err := json.Marshal(c)
		if err != nil {
			panic(err)
		}
		f.chunks <- raw
	}
	close(f.chunks)
	f.end <- end
	close(f.end)
	return f
}

func (f *fakeStartStream) Chunks() <-chan json.RawMessage { return f.chunks }
func (f *fakeStartStream) End() <-chan client.End         { return f.end }

func TestStartingChunksAreNotOutcomes(t *testing.T) {
	endData, err := json.Marshal(rpc.GroupsStartEnd{
		Started: []string{"api"}, Skipped: []string{"worker"}, Errors: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	stream := newFakeStartStream([]rpc.GroupsStartChunk{
		{Service: "api", State: "starting", Phase: "waiting_dependency", WaitingOn: []string{"db"}, ReadyTimeout: "30s", ElapsedMS: 15},
		{Service: "api", State: "starting", Phase: "prepare", LastPrepareMS: int64Ptr(800), ElapsedMS: 900},
		{Service: "api", State: "started", PID: 100, Port: 5000, ReadyTimeout: "30s"},
		{Service: "worker", State: "skipped", Skipped: true, Reason: "already running"},
	}, client.End{Data: endData})

	var progress bytes.Buffer
	chunks, summary, err := collectStartChunks(stream, &progress, false, true)
	if err != nil {
		t.Fatalf("collectStartChunks: %v", err)
	}
	// Everything downstream — the readiness wait, services[] in JSON and the
	// summary — sees only the two outcomes.
	if len(chunks) != 2 {
		t.Fatalf("chunks = %+v, want only the two outcomes", chunks)
	}
	for _, c := range chunks {
		if c.State == "starting" {
			t.Fatalf("a starting chunk survived into services[]: %+v", c)
		}
	}
	if len(summary.Started) != 1 || len(summary.Skipped) != 1 || len(summary.Errors) != 0 {
		t.Fatalf("summary = %+v", summary)
	}

	// Progress is a diagnostic on the writer (stderr in production); it names
	// the phase, what is being waited on, and the previous prepare duration.
	got := progress.String()
	for _, want := range []string{"api", "waiting_dependency", "db", "prepare", "800", "900ms"} {
		if !strings.Contains(got, want) {
			t.Fatalf("progress = %q, missing %q", got, want)
		}
	}
}

func TestStartingChunkClassification(t *testing.T) {
	if !isStartProgressChunk(rpc.GroupsStartChunk{State: "starting"}) {
		t.Fatal("a starting chunk was not classified as progress")
	}
	for _, state := range []string{"started", "skipped", "failed", "ready_timeout", ""} {
		if isStartProgressChunk(rpc.GroupsStartChunk{State: state}) {
			t.Fatalf("state %q was classified as progress", state)
		}
	}
}

func TestStartProgressLineIsDimAndQuietOnEmptyPhase(t *testing.T) {
	var out bytes.Buffer
	printStartProgress(&out, rpc.GroupsStartChunk{Service: "api", State: "starting", Phase: "spawning"})
	if !strings.Contains(out.String(), "api") || !strings.Contains(out.String(), "spawning") {
		t.Fatalf("progress line = %q", out.String())
	}
}

func int64Ptr(v int64) *int64 { return &v }
