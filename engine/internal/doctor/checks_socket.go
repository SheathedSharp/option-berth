package doctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
)

// checkSocketPermissions reads the socket's mode and owner back off the disk:
// both must belong to this user alone, and the directory option-berth creates
// for it must be 0700. Windows has neither — its named pipe carries an ACL
// applied at bind time and no file to stat — so the row skips instead of
// inventing an equivalent.
func checkSocketPermissions(ctx context.Context, env *Env) rpc.DoctorCheck {
	socket := env.SocketPath
	if env.GOOS == "windows" || strings.HasPrefix(socket, `\\`) {
		return skip("named pipe, not a file",
			"on Windows the daemon listens on "+daemon.WindowsPipe+
				", whose DACL grants only this user and SYSTEM when it is bound; there are no file modes to check")
	}
	if socket == "" {
		return skip("no socket path resolved", "")
	}

	info, err := os.Stat(socket)
	if os.IsNotExist(err) {
		if env.Daemon(ctx).Reachable {
			return fail("the daemon answers but its socket is gone",
				socket+" does not exist", "oberth daemon restart")
		}
		return skip("no socket to check", socket+" appears when the daemon starts")
	}
	if err != nil {
		return fail("cannot stat the socket", err.Error(), "")
	}

	note, problems := socketFacts(info, env, socket)
	if len(problems) > 0 {
		return fail("the socket is reachable by other users",
			fmt.Sprintf("%s: %s", socket, strings.Join(problems, "; ")),
			fmt.Sprintf("chmod 700 %s && chmod 600 %s", filepath.Dir(socket), socket))
	}
	return ok(fmt.Sprintf("socket is %04o and yours", info.Mode().Perm()), socket+note)
}

// socketFacts checks the three things a reader must trust: the socket mode, its
// owner, and — when the daemon owns the directory — the directory mode. It
// returns a readable note for the healthy case and one line per problem.
func socketFacts(info os.FileInfo, env *Env, socket string) (note string, problems []string) {
	mode := info.Mode().Perm()
	if mode&0o077 != 0 {
		problems = append(problems, fmt.Sprintf("mode is %04o, want 0600", mode))
	}
	if owner, known := fileOwner(info); known && env.UID >= 0 && owner != env.UID {
		problems = append(problems, fmt.Sprintf("owned by uid %d, you are uid %d", owner, env.UID))
	}
	dir := filepath.Dir(socket)
	if !daemon.OwnsSocketDir(socket) {
		return "", problems
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		return "", problems
	}
	dirMode := dirInfo.Mode().Perm()
	if dirMode&0o077 != 0 {
		problems = append(problems, fmt.Sprintf("%s is %04o, want 0700", dir, dirMode))
		return "", problems
	}
	return fmt.Sprintf(", %s is %04o", dir, dirMode), problems
}
