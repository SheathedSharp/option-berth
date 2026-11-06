package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
)

// doctorRequest builds the request the daemon dispatch would hand to
// `daemon.doctor`, with a runtime that answers from its own process.
func doctorRequest(t *testing.T, params rpc.DaemonDoctorParams) *daemon.Request {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	return &daemon.Request{
		Runtime: &daemon.Runtime{Version: "v1.2.3", Socket: "/tmp/doctor-fixture.sock", PID: 4242},
		Params:  raw,
	}
}

func TestHandleDoctorAnswersFromTheDaemon(t *testing.T) {
	req := doctorRequest(t, rpc.DaemonDoctorParams{Only: []string{"cli_on_path", "daemon_reachable"}})
	reply, err := handleDoctor(context.Background(), req)
	if err != nil {
		t.Fatalf("handleDoctor: %v", err)
	}
	result, ok := reply.(rpc.DaemonDoctorResult)
	if !ok {
		t.Fatalf("reply type = %T, want DaemonDoctorResult", reply)
	}
	if len(result.Checks) != 2 {
		t.Fatalf("checks = %v, want two rows", result.Checks)
	}

	wantStatus(t, result.Checks[0], StatusSkip)
	if !strings.Contains(result.Checks[0].Detail, "CLI-only") {
		t.Errorf("cli_on_path detail = %q, want the skip reason", result.Checks[0].Detail)
	}
	wantStatus(t, result.Checks[1], StatusOK)
	if !strings.Contains(result.Checks[1].Detail, "4242") {
		t.Errorf("daemon_reachable detail = %q, want the daemon pid", result.Checks[1].Detail)
	}
	if result.Version != "v1.2.3" || result.DaemonVersion != "v1.2.3" {
		t.Errorf("version = %q, daemon version = %q", result.Version, result.DaemonVersion)
	}
}

func TestHandleDoctorRejectsBadParams(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params rpc.DaemonDoctorParams
		want   string
	}{
		{"unknown selector", rpc.DaemonDoctorParams{Only: []string{"nope"}}, "unknown check"},
		{"relative project", rpc.DaemonDoctorParams{Project: "."}, "absolute path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := handleDoctor(context.Background(), doctorRequest(t, tc.params))
			var rpcErr *rpc.Error
			if !errors.As(err, &rpcErr) || rpcErr.Code != rpc.CodeInvalidParams {
				t.Fatalf("error = %v, want invalid_params", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}
