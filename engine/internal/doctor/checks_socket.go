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

// checkSocketPermissions is contract §21 read back off the disk: the socket
// must be a same-user-only file, and the directory option-berth makes for it must be
// 0700. Windows has neither — the pipe's ACL is applied at bind time and there
// is no file to stat — so the check skips rather than inventing an equivalent.
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

	var problems []string
	mode := info.Mode().Perm()
	if mode&0o077 != 0 {
		problems = append(problems, fmt.Sprintf("mode is %04o, want 0600", mode))
	}
	if uid, ok := fileOwner(info); ok && env.UID >= 0 && uid != env.UID {
		problems = append(problems, fmt.Sprintf("owned by uid %d, you are uid %d", uid, env.UID))
	}

	dir := filepath.Dir(socket)
	dirNote := ""
	if daemon.OwnsSocketDir(socket) {
		if dirInfo, err := os.Stat(dir); err == nil {
			if dirMode := dirInfo.Mode().Perm(); dirMode&0o077 != 0 {
				problems = append(problems, fmt.Sprintf("%s is %04o, want 0700", dir, dirMode))
			} else {
				dirNote = fmt.Sprintf(", %s is %04o", dir, dirMode)
			}
		}
	}

	if len(problems) > 0 {
		return fail("the socket is reachable by other users",
			fmt.Sprintf("%s: %s", socket, strings.Join(problems, "; ")),
			fmt.Sprintf("chmod 700 %s && chmod 600 %s", dir, socket))
	}
	return ok(fmt.Sprintf("socket is %04o and yours", mode), socket+dirNote)
}
