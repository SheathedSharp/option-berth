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

func checkDaemonReachable(ctx context.Context, env *Env) rpc.DoctorCheck {
	info := env.Daemon(ctx)
	switch {
	case info.Local:
		return ok("the daemon is running",
			fmt.Sprintf("this answer came from the daemon itself (pid %d, %s)", info.PID, info.Socket))
	case info.Reachable:
		return ok("the daemon is listening", fmt.Sprintf("pid %d on %s", info.PID, info.Socket))
	}
	detail := env.SocketPath
	if info.Err != nil {
		detail = fmt.Sprintf("%s: %v", env.SocketPath, info.Err)
	}
	return failFixable("no daemon is listening", detail, "oberth serve --detach")
}

func checkDaemonVersionMatches(ctx context.Context, env *Env) rpc.DoctorCheck {
	info := env.Daemon(ctx)
	if !info.Reachable {
		return skip("no daemon to compare against", "start the daemon and run doctor again")
	}
	if info.Version == env.Version {
		return ok(fmt.Sprintf("daemon and CLI are both %s", env.Version), "")
	}
	return warn(fmt.Sprintf("daemon is %s, CLI is %s", info.Version, env.Version),
		"an old daemon keeps serving whoever started it; restarting it "+
			"adopts the version of the binary you are running now",
		"oberth daemon restart")
}

func checkDaemonProtocol(ctx context.Context, env *Env) rpc.DoctorCheck {
	info := env.Daemon(ctx)
	if !info.Reachable {
		return skip("no daemon to handshake with", "start the daemon and run doctor again")
	}
	got, gotErr := protocolMajor(info.ProtocolVersion)
	want, wantErr := protocolMajor(rpc.ProtocolVersion)
	if gotErr != nil || wantErr != nil {
		return fail("unreadable protocol version",
			fmt.Sprintf("daemon reported %q, this build speaks %q", info.ProtocolVersion, rpc.ProtocolVersion),
			"oberth daemon restart")
	}
	if got != want {
		return fail(fmt.Sprintf("protocol major %d, this build speaks %d", got, want),
			fmt.Sprintf("daemon protocol %s, client protocol %s; only the major has to match",
				info.ProtocolVersion, rpc.ProtocolVersion),
			"oberth daemon restart")
	}
	return ok(fmt.Sprintf("protocol %s", info.ProtocolVersion),
		fmt.Sprintf("this build speaks %s; majors match", rpc.ProtocolVersion))
}

// protocolMajor returns the leading integer of a protocol version. Only the
// major has to agree between a daemon and this build.
func protocolMajor(version string) (int, error) {
	text := strings.TrimPrefix(strings.TrimSpace(version), "v")
	if dot := strings.IndexByte(text, '.'); dot >= 0 {
		text = text[:dot]
	}
	return strconv.Atoi(text)
}

// checkDBOK opens the database the daemon uses and reports its schema version
// and size. It never creates or migrates one: a machine whose daemon has never
// run has no database, which is normal, and a diagnosis must not change the
// patient.
func checkDBOK(_ context.Context, env *Env) rpc.DoctorCheck {
	path := env.DBPath
	if path == "" {
		return skip("no database path resolved", "")
	}
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return warn("no database yet",
			path+" is created the first time the daemon runs", "oberth serve --detach")
	}
	if err != nil {
		return fail("cannot stat the database", err.Error(), "")
	}

	db, err := store.OpenReadOnly(path)
	if err != nil {
		return fail("cannot open the database", fmt.Sprintf("%s: %v", path, err),
			"preserve the database and inspect it before choosing a repair")
	}
	defer db.Close()

	version, err := db.Version()
	if err != nil {
		return fail("cannot read the schema version", fmt.Sprintf("%s: %v", path, err), "")
	}
	latest := store.LatestVersion()
	size := humanBytes(info.Size())
	if version > latest {
		return fail(fmt.Sprintf("schema v%d is newer than supported v%d", version, latest),
			path+" was left unchanged",
			"upgrade the CLI to match the database; do not restart an older daemon")
	}
	if version != latest {
		return warn(fmt.Sprintf("schema v%d, this build expects v%d", version, latest),
			fmt.Sprintf("%s (%s)", path, size),
			"oberth daemon restart — the daemon migrates on open")
	}
	return ok(fmt.Sprintf("schema v%d, %s", version, size), path)
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
