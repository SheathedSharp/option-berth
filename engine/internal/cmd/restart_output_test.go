package cmd

// This suite calls only the production stop/start result adapter and renderers.
// Its RPC callbacks are in-memory; it never invokes a command, runtime service or signal.
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/restartout"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

func captureRestartOutput(t *testing.T, run func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old; _ = r.Close(); _ = w.Close() }()
	result := run()
	_ = w.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(data), result
}

func TestRestartProductionReceiptsAndErrorProtocol(t *testing.T) {
	transport := errors.New("synthetic stream lost")
	ready := false
	partial := restartout.Start{Initial: rpc.GroupsStartResult{StartID: "request1"}, Chunks: []rpc.GroupsStartChunk{{Service: "workerB", State: "started", RunID: "runB", Ready: &ready}}}
	success := partial
	success.End = rpc.GroupsStartEnd{Started: []string{"workerB"}, Skipped: []string{}, Errors: []string{}}
	failed := restartout.Start{Chunks: []rpc.GroupsStartChunk{{Service: "workerB", State: "failed", Error: "synthetic start blocked"}}, End: rpc.GroupsStartEnd{Started: []string{}, Skipped: []string{}, Errors: []string{"workerB"}}}
	cases := []struct {
		name            string
		stopItemsOK     bool
		stopOK          bool
		stopErr         error
		start           restartout.Start
		startErr        error
		cancelAfterStop bool
		preCancelled    bool
		emptyStopResult bool
		wantStart       bool
		wantReceipt     bool
		wantCode        int
	}{
		{name: "normal", stopOK: true, start: success, wantStart: true, wantReceipt: true},
		{name: "stop_failed", wantReceipt: true, wantCode: 1},
		{name: "stop_failed_no_targets", emptyStopResult: true, wantReceipt: true, wantCode: 1},
		{name: "start_business_failed", stopOK: true, start: failed, wantStart: true, wantReceipt: true, wantCode: 1},
		{name: "stop_transport_without_receipt", stopErr: transport, emptyStopResult: true, wantCode: 1},
		{name: "stop_transport_partial", stopErr: transport, wantReceipt: true, wantCode: 1},
		{name: "cancel_partial_stop_receipt", stopItemsOK: true, stopErr: context.Canceled, wantReceipt: true, wantCode: 130},
		{name: "cancel_after_stop", stopOK: true, cancelAfterStop: true, wantReceipt: true, wantCode: 130},
		{name: "start_transport_before_reply", stopOK: true, startErr: transport, wantStart: true, wantReceipt: true, wantCode: 1},
		{name: "start_transport_partial", stopOK: true, start: partial, startErr: transport, wantStart: true, wantReceipt: true, wantCode: 1},
		{name: "cancel_during_start_partial", stopOK: true, start: partial, startErr: context.Canceled, wantStart: true, wantReceipt: true, wantCode: 130},
		{name: "cancel_before_stop", preCancelled: true, wantCode: 130},
	}
	for _, jsonOutput := range []bool{true, false} {
		for _, tc := range cases {
			t.Run(tc.name+map[bool]string{true: "/json", false: "/human"}[jsonOutput], func(t *testing.T) {
				oldJSON, oldMode := restartJSON, jsonMode
				restartJSON, jsonMode = jsonOutput, jsonOutput
				defer func() { restartJSON, jsonMode = oldJSON, oldMode }()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if tc.preCancelled {
					cancel()
				}
				stopCalled, startCalled := false, false
				stopped := rpc.KillEnvelope{MutationResult: rpc.MutationResult{OK: tc.stopOK, Affected: []string{"workerA"}}, Results: []state.KillResult{{PID: 42, Name: "workerA", OK: tc.stopOK, Error: "synthetic stop blocked"}}}
				if tc.stopOK || tc.stopItemsOK {
					stopped.Results[0].OK = true
					stopped.Results[0].Error = ""
				}
				if tc.emptyStopResult {
					stopped = rpc.KillEnvelope{}
				}
				stdout, opErr := captureRestartOutput(t, func() error {
					return restartout.Execute(ctx,
						func(context.Context) (rpc.KillEnvelope, error) {
							stopCalled = true
							if tc.cancelAfterStop {
								cancel()
							}
							return stopped, tc.stopErr
						},
						func(context.Context) (restartout.Start, error) { startCalled = true; return tc.start, tc.startErr },
						func(doc restartout.Document, complete bool) error { return reportRestart(doc, complete, nil) })
				})
				if stopCalled == tc.preCancelled || startCalled != tc.wantStart {
					t.Fatalf("stop/start calls=%v/%v", stopCalled, startCalled)
				}
				var stderr bytes.Buffer
				code := 0
				if opErr != nil {
					code = reportError(&stderr, opErr)
				}
				if code != tc.wantCode {
					t.Fatalf("exit=%d want=%d error=%v stderr=%s stdout=%s", code, tc.wantCode, opErr, stderr.String(), stdout)
				}
				if tc.startErr != nil && !errors.Is(opErr, tc.startErr) {
					t.Fatalf("start error lost: %v", opErr)
				}
				if tc.stopErr != nil && !errors.Is(opErr, tc.stopErr) {
					t.Fatalf("stop error lost: %v", opErr)
				}
				if tc.cancelAfterStop && !errors.Is(opErr, context.Canceled) {
					t.Fatalf("cancellation lost: %v", opErr)
				}
				if (tc.startErr != nil || tc.stopErr != nil || tc.cancelAfterStop || tc.preCancelled) && stderr.Len() == 0 {
					t.Fatal("operation error hidden on stderr")
				}
				if !tc.wantReceipt {
					if stdout != "" {
						t.Fatal("invented receipt before stop:", stdout)
					}
					return
				}
				if jsonOutput {
					dec := json.NewDecoder(strings.NewReader(stdout))
					var doc restartout.Document
					if err := dec.Decode(&doc); err != nil {
						t.Fatalf("missing receipt: %v; %q", err, stdout)
					}
					var extra any
					if err := dec.Decode(&extra); err != io.EOF {
						t.Fatalf("stdout is not exactly one value: %v", err)
					}
					if len(doc.Stopped.Results) != len(stopped.Results) || (len(doc.Stopped.Results) > 0 && doc.Stopped.Results[0].PID != 42) || doc.Stopped.OK != tc.stopOK {
						t.Fatalf("lost stopped receipt: %+v", doc)
					}
					if len(doc.Services) != len(tc.start.Chunks) || doc.Services == nil {
						t.Fatalf("lost partial services: %+v", doc)
					}
					if len(doc.Services) > 0 {
						got, _ := json.Marshal(doc.Services)
						want, _ := json.Marshal(tc.start.Chunks)
						if !bytes.Equal(got, want) {
							t.Fatalf("changed per-service receipt: %s / %s", got, want)
						}
					}
					if tc.startErr != nil || tc.cancelAfterStop {
						if doc.Started != nil || doc.Skipped != nil || doc.Errors != nil {
							t.Fatal("invented terminal summary")
						}
					}
					if tc.wantCode == 0 && (len(doc.Started) != 1 || doc.Started[0] != "workerB") {
						t.Fatal("normal summary changed")
					}
					if tc.wantCode != 0 && (tc.startErr != nil || tc.stopErr != nil || tc.cancelAfterStop) {
						var errorDoc struct {
							Error struct {
								Code string `json:"code"`
							} `json:"error"`
						}
						if err := json.Unmarshal(stderr.Bytes(), &errorDoc); err != nil || errorDoc.Error.Code == "" {
							t.Fatalf("stderr error protocol lost: %s", stderr.String())
						}
						if tc.wantCode == 130 && errorDoc.Error.Code != "interrupted" {
							t.Fatal("interrupted code lost")
						}
					}
				} else {
					if tc.stopOK && !strings.Contains(stdout, "1/1 stopped.") {
						t.Fatalf("missing human stopped receipt: %q", stdout)
					}
					if len(tc.start.Chunks) > 0 && !strings.Contains(stdout, "workerB") {
						t.Fatalf("missing human start receipt: %q", stdout)
					}
					if (tc.startErr != nil || tc.cancelAfterStop) && strings.Contains(stdout, "0 started") {
						t.Fatal("printed a fabricated success summary")
					}
				}
			})
		}
	}
}

func TestRestartOutputFailureRetainsOperationError(t *testing.T) {
	writeErr := errors.New("synthetic output unavailable")
	called := false
	err := restartout.Execute(context.Background(), func(context.Context) (rpc.KillEnvelope, error) {
		return rpc.KillEnvelope{MutationResult: rpc.MutationResult{OK: true}}, nil
	}, func(context.Context) (restartout.Start, error) { return restartout.Start{}, context.Canceled }, func(restartout.Document, bool) error { called = true; return writeErr })
	if !called || !errors.Is(err, context.Canceled) || !errors.Is(err, writeErr) {
		t.Fatalf("receipt/error ownership lost: %v", err)
	}
}
