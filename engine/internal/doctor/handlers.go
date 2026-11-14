package doctor

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
)

const doctorMethod = "runtime.service.doctor"

func init() {
	runtimesvc.RegisterHandler(doctorMethod, handleDoctor)
	runtimesvc.RegisterCapability("doctor")
}

// `runtime.service.doctor` lets the desktop app diagnose an installation without
// shelling out to a CLI it may not have found. It runs the same table as
// `oberth doctor`; rows that describe the invoking process come back as `skip`
// with a reason. The handler lives here so the runtime service package never imports the
// doctor.
func handleDoctor(ctx context.Context, req *runtimesvc.Request) (any, error) {
	var params rpc.RuntimeServiceDoctorParams
	if err := req.Bind(&params); err != nil {
		return nil, err
	}
	if err := doctorParamsProblem(params); err != nil {
		return nil, err
	}
	return Run(ctx, runtimeServiceEnv(req.HostContext, params), params.Only), nil
}

// doctorParamsProblem rejects a request the runtime service cannot answer as asked,
// before any check runs.
func doctorParamsProblem(params rpc.RuntimeServiceDoctorParams) error {
	if bad := UnknownSelectors(params.Only); len(bad) > 0 {
		return rpc.NewError(rpc.CodeInvalidParams,
			"unknown check "+strings.Join(bad, ", "),
			"known checks: "+strings.Join(append(IDs(), Prefixes()...), ", "))
	}
	if params.Project != "" && !filepath.IsAbs(params.Project) {
		return rpc.NewError(rpc.CodeInvalidParams,
			"project must be an absolute path",
			"the runtime service has its own working directory, so a relative path would mean something else here")
	}
	return nil
}

// runtimeServiceEnv describes this runtime service process from the inside: its own identity,
// its own paths, and no CLI-only seams.
func runtimeServiceEnv(rt *runtimesvc.HostContext, params rpc.RuntimeServiceDoctorParams) Env {
	self := RuntimeServiceInfo{Reachable: true, Local: true, PID: rt.PID}
	self.Version = rt.Version
	self.ProtocolVersion = rpc.ProtocolVersion
	self.Socket = rt.Socket
	self.DBPath = rt.DBPath()
	return Env{
		Mode:           ModeRuntimeService,
		Version:        rt.Version,
		Project:        params.Project,
		SocketPath:     rt.Socket,
		DBPath:         rt.DBPath(),
		ReadDatabase:   func(ctx context.Context) (rpc.RuntimeStorageSchema, error) { return InspectDatabase(ctx, rt.Store) },
		RuntimeService: func(context.Context) RuntimeServiceInfo { return self },
		Docker:         DockerProbe,
	}
}
