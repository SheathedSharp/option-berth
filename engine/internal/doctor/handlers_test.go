package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
)

// doctorRequest builds the request the runtime service dispatch would hand to
// `runtime.service.doctor`, with a runtime that answers from its own process.
func doctorRequest(t *testing.T, params rpc.RuntimeServiceDoctorParams) *runtimesvc.Request {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	return &runtimesvc.Request{
		HostContext: &runtimesvc.HostContext{Version: "v1.2.3", Socket: "/tmp/doctor-fixture.sock", PID: 4242},
		Params:      raw,
	}
}

func TestHandleDoctorAnswersFromTheRuntimeService(t *testing.T) {
	req := doctorRequest(t, rpc.RuntimeServiceDoctorParams{Only: []string{"cli_on_path", "runtime_service_reachable"}})
	reply, err := handleDoctor(context.Background(), req)
	if err != nil {
		t.Fatalf("handleDoctor: %v", err)
	}
	result, ok := reply.(rpc.RuntimeServiceDoctorResult)
	if !ok {
		t.Fatalf("reply type = %T, want RuntimeServiceDoctorResult", reply)
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
		t.Errorf("runtime_service_reachable detail = %q, want the runtime service pid", result.Checks[1].Detail)
	}
	if result.Version != "v1.2.3" || result.RuntimeServiceVersion != "v1.2.3" {
		t.Errorf("version = %q, runtime service version = %q", result.Version, result.RuntimeServiceVersion)
	}
}

func TestHandleDoctorRejectsBadParams(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params rpc.RuntimeServiceDoctorParams
		want   string
	}{
		{"unknown selector", rpc.RuntimeServiceDoctorParams{Only: []string{"nope"}}, "unknown check"},
		{"relative project", rpc.RuntimeServiceDoctorParams{Project: "."}, "absolute path"},
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
