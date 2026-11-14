package doctor

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

func checkRuntimeServiceReachable(ctx context.Context, env *Env) rpc.DoctorCheck {
	info := env.RuntimeService(ctx)
	switch {
	case info.Local:
		return ok("the runtime service is running",
			fmt.Sprintf("this answer came from the runtime service itself (pid %d, %s)", info.PID, info.Socket))
	case info.Reachable:
		return ok("the runtime service is listening", fmt.Sprintf("pid %d on %s", info.PID, info.Socket))
	}
	detail := env.SocketPath
	if info.Err != nil {
		detail = fmt.Sprintf("%s: %v", env.SocketPath, info.Err)
	}
	return failFixable("no runtime service is listening", detail, "oberth runtime service run --detach")
}

func checkRuntimeServiceVersionMatches(ctx context.Context, env *Env) rpc.DoctorCheck {
	info := env.RuntimeService(ctx)
	if !info.Reachable {
		return skip("no runtime service to compare against", "start the runtime service and run doctor again")
	}
	if info.Version == env.Version {
		return ok(fmt.Sprintf("runtime service and CLI are both %s", env.Version), "")
	}
	return warn(fmt.Sprintf("runtime service is %s, CLI is %s", info.Version, env.Version),
		"an old runtime service keeps serving whoever started it; restarting it "+
			"adopts the version of the binary you are running now",
		"oberth runtime service restart")
}

func checkRuntimeServiceProtocol(ctx context.Context, env *Env) rpc.DoctorCheck {
	info := env.RuntimeService(ctx)
	if !info.Reachable {
		return skip("no runtime service to handshake with", "start the runtime service and run doctor again")
	}
	got, gotErr := protocolMajor(info.ProtocolVersion)
	want, wantErr := protocolMajor(rpc.ProtocolVersion)
	if gotErr != nil || wantErr != nil {
		return fail("unreadable protocol version",
			fmt.Sprintf("runtime service reported %q, this build speaks %q", info.ProtocolVersion, rpc.ProtocolVersion),
			"oberth runtime service restart")
	}
	if got != want {
		return fail(fmt.Sprintf("protocol major %d, this build speaks %d", got, want),
			fmt.Sprintf("runtime service protocol %s, client protocol %s; only the major has to match",
				info.ProtocolVersion, rpc.ProtocolVersion),
			"oberth runtime service restart")
	}
	return ok(fmt.Sprintf("protocol %s", info.ProtocolVersion),
		fmt.Sprintf("this build speaks %s; majors match", rpc.ProtocolVersion))
}

// protocolMajor returns the leading integer of a protocol version. Only the
// major has to agree between a runtime service and this build.
func protocolMajor(version string) (int, error) {
	text := strings.TrimPrefix(strings.TrimSpace(version), "v")
	if dot := strings.IndexByte(text, '.'); dot >= 0 {
		text = text[:dot]
	}
	return strconv.Atoi(text)
}

// checkDBOK opens the database the runtime service uses and reports its schema version
// and size. It never creates or migrates one: a machine whose runtime service has never
// run has no database, which is normal, and a diagnosis must not change the
// patient.
func checkDBOK(ctx context.Context, env *Env) rpc.DoctorCheck {
	path := env.DBPath
	if path == "" {
		return skip("no database path resolved", "")
	}
	var info rpc.RuntimeStorageSchema
	var err error
	if env.ReadDatabase != nil {
		info, err = env.ReadDatabase(ctx)
	} else {
		var db *store.Store
		db, err = store.OpenReadOnly(path)
		if err == nil {
			info, err = InspectDatabase(ctx, db)
			_ = db.Close()
		}
	}
	if os.IsNotExist(err) {
		return warn("no database yet", path+" is created the first time the runtime service runs", "oberth runtime service run --detach")
	}
	if err != nil {
		return fail("cannot read the database", fmt.Sprintf("%s: %v", path, err), "preserve the database and inspect it before choosing a repair")
	}
	version, latest := info.Version, store.LatestVersion()
	size := humanBytes(info.Size)
	if version > latest {
		return fail(fmt.Sprintf("schema v%d is newer than supported v%d", version, latest), info.Path+" was left unchanged", "upgrade the CLI to match the database; do not restart an older runtime service")
	}
	if version != latest {
		return warn(fmt.Sprintf("schema v%d, this build expects v%d", version, latest), fmt.Sprintf("%s (%s)", info.Path, size), "oberth runtime service restart — the runtime service migrates on open")
	}
	return ok(fmt.Sprintf("schema v%d, %s", version, size), info.Path)
}

// InspectDatabase reads the already-owning handle without opening another one.
func InspectDatabase(ctx context.Context, db *store.Store) (rpc.RuntimeStorageSchema, error) {
	if db == nil {
		return rpc.RuntimeStorageSchema{}, fmt.Errorf("Runtime Service storage is unavailable")
	}
	version, err := db.VersionContext(ctx)
	if err != nil {
		return rpc.RuntimeStorageSchema{}, err
	}
	info, err := os.Stat(db.DBPath())
	if err != nil {
		return rpc.RuntimeStorageSchema{}, err
	}
	return rpc.RuntimeStorageSchema{Path: db.DBPath(), Version: version, Size: info.Size()}, nil
}

// byteUnits is the ladder humanBytes climbs: bytes, then the binary prefixes.
var byteUnits = [...]string{"B", "KiB", "MiB", "GiB", "TiB"}

func humanBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	value := float64(n)
	unit := 0
	for value >= 1024 && unit < len(byteUnits)-1 {
		value /= 1024
		unit++
	}
	return fmt.Sprintf("%.1f %s", value, byteUnits[unit])
}
