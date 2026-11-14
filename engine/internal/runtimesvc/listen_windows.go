//go:build windows

package runtimesvc

import (
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// pipeBindTimeout caps the retry in listen. It is generous on purpose: a
// restart is already waiting on the outgoing daemon's lock, so spending an
// extra second here only turns a failed restart into one that succeeds.
const pipeBindTimeout = 5 * time.Second

// listen binds the named pipe under a security descriptor that grants full
// access to the current user and to SYSTEM and to nobody else — the Windows
// counterpart of the 0600 unix socket (spec, "Transport details").
//
// A pipe instance outlives the process that created it for as long as Windows
// needs to tear the last instance down, so a daemon replacing another one can
// meet "Access is denied" on a name that is about to become free. That is a
// race rather than a refusal: retry for a few seconds before giving up.
func listen(path string) (net.Listener, error) {
	sddl, err := userOnlySDDL()
	if err != nil {
		return nil, err
	}
	pipeCfg := &winio.PipeConfig{SecurityDescriptor: sddl}
	deadline := time.Now().Add(pipeBindTimeout)
	for {
		listener, err := winio.ListenPipe(path, pipeCfg)
		if err == nil {
			return listener, nil
		}
		if !pipeNameStillBusy(err) || !time.Now().Before(deadline) {
			return nil, err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// pipeNameStillBusy reports whether creating the pipe failed only because the
// previous owner has not finished releasing the name.
func pipeNameStillBusy(cause error) bool {
	return errors.Is(cause, windows.ERROR_ACCESS_DENIED) ||
		errors.Is(cause, windows.ERROR_PIPE_BUSY)
}

// dial connects to the named pipe. The short timeout matters: a pipe with no
// server answers immediately instead of blocking.
func dial(path string) (net.Conn, error) {
	wait := 2 * time.Second
	return winio.DialPipe(path, &wait)
}

// removeStaleSocket does nothing on Windows: a named pipe vanishes together
// with the process that created it, so no stale entry can be left behind.
func removeStaleSocket(string) error { return nil }

// userOnlySDDL builds "D:P(A;;GA;;;<user SID>)(A;;GA;;;SY)": a protected DACL
// that grants generic-all to the daemon's own user and to LocalSystem.
func userOnlySDDL() (string, error) {
	processToken := windows.GetCurrentProcessToken()
	tokenUser, err := processToken.GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("reading process token: %w", err)
	}
	userSID := tokenUser.User.Sid.String()
	if userSID == "" {
		return "", fmt.Errorf("process token has no user SID")
	}
	return fmt.Sprintf("D:P(A;;GA;;;%s)(A;;GA;;;SY)", userSID), nil
}
