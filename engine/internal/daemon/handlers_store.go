package daemon

import (
	"context"
	"errors"

	"github.com/sheathedsharp/option-berth/internal/config"
	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/internal/scanner"
	"github.com/sheathedsharp/option-berth/internal/state"
	"github.com/sheathedsharp/option-berth/internal/store"
)

// The write paths that live in the database, plus the settings file behind
// them. The read side of ports.* belongs to step 1A.6 and registers itself.
func init() {
	RegisterHandler("config.get", handleConfigGet)
	RegisterHandler("config.set", handleConfigSet)
	RegisterHandler("config.path", handleConfigPath)
}

// errNoStore is what every database-backed method reports when the daemon came
// up without one.
func errNoStore() error {
	return rpc.NewError(rpc.CodeInternal,
		"this daemon has no database open",
		"check `oberth daemon log` for the error opening option-berth.db, then `oberth daemon restart`")
}

func handleConfigGet(_ context.Context, _ *Request) (any, error) {
	cfg, err := config.Map()
	if err != nil {
		return nil, rpc.NewError(rpc.CodeInternal, err.Error(),
			"fix or remove "+config.Path())
	}
	// A `decide:` block left over from the retired judgment layer can hold an
	// API key. Nothing reads it any more, so it is not sent either.
	delete(cfg, "decide")
	return rpc.ConfigGetResult{Config: cfg}, nil
}

func handleConfigSet(_ context.Context, req *Request) (any, error) {
	var p rpc.ConfigSetParams
	if err := req.Bind(&p); err != nil {
		return nil, err
	}
	if len(p.Patch) == 0 {
		return nil, rpc.NewError(rpc.CodeInvalidParams, "patch is required",
			`send {"patch": {"list": {"sort": "port"}}}; a null value clears a key`)
	}
	cfg, err := config.Apply(p.Patch)
	if err != nil {
		return nil, rpc.NewError(rpc.CodeInvalidParams, err.Error(),
			"the config was left as it was; `oberth config path` shows the file")
	}
	req.Runtime.Logger.Info("config written", "path", config.Path())
	return rpc.ConfigSetResult{OK: true, Config: cfg}, nil
}

func handleConfigPath(_ context.Context, _ *Request) (any, error) {
	return rpc.ConfigPathResult{Path: config.Path()}, nil
}

// republish makes a write visible before its own reply is. It re-attributes
// the last scan and publishes synchronously, so by the time the handler
// returns the delta carrying the change is already queued on every
// subscriber's connection — ahead of this call's response, which the same
// writer queues afterwards — and the caller's own next read is served from a
// snapshot that has it.
//
// Contract §18 only asks that a rename or an assign take effect "in the next
// publish", with an immediate rescan so the caller sees it. Replying *after*
// that publish is what turns "the next delta carries the rename" from a race
// into a guarantee: a subscriber cannot receive the acknowledgement before the
// delta that justifies it, and a client that reads straight after the reply
// cannot be served the state from before its own write.
//
// It does not scan the machine. A rename, a group pin and a `oberth.yaml` edit
// change how the ports the daemon already knows are named and grouped, not
// which ports exist, so re-running attribution over the last scan's own rows
// answers the question — and it does so in microseconds instead of behind
// `lsof`, `ps` and `docker stats` (contract §44). `Republish` wakes the loop,
// so a real scan follows.
func republish(rt *Runtime) {
	if err := rt.Scanner.Republish(); err != nil {
		rt.Logger.Warn("republishing after a write", "error", err)
	}
}

// rescanAfterKill is republish's sibling for the one write that does change
// which ports exist. A kill has to be followed by a real scan: the ports that
// just went away are only gone from the snapshot once the OS has been asked
// again, and their port_down rows reach the history ring from that delta.
func rescanAfterKill(rt *Runtime) (state.Snapshot, error) {
	snap, err := rt.Scanner.Rescan(scanner.Include{})
	if err != nil {
		rt.Logger.Warn("rescanning after a kill", "error", err)
	}
	return snap, err
}

// storeError wraps a database failure as an internal error with the path.
func storeError(what string, err error) error {
	if errors.Is(err, store.ErrInvalidName) {
		return rpc.NewError(rpc.CodeInvalidParams, err.Error(),
			"names are a single word: no whitespace, no / and no \\")
	}
	return rpc.NewError(rpc.CodeInternal, what+": "+err.Error(),
		"check `oberth daemon log`")
}
