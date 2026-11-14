package runtimesvc

import (
	"context"
	"errors"

	"github.com/sheathedsharp/option-berth/engine/internal/config"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// The database-backed write paths, together with the settings file behind them.
// The read side of ports.* lives elsewhere and registers itself.
func init() {
	registerSettingsHandlers()
}

func registerSettingsHandlers() {
	RegisterHandler("config.get", handleConfigGet)
	RegisterHandler("config.set", handleConfigSet)
	RegisterHandler("config.path", handleConfigPath)
}

// errNoStore is the reply of every database-backed method when the runtime service came
// up with no database.
func errNoStore() error {
	return rpc.NewError(rpc.CodeInternal, "this runtime service has no database open",
		"check `oberth runtime service log` for the error opening option-berth.db, then `oberth runtime service restart`")
}

func handleConfigGet(_ context.Context, _ *Request) (any, error) {
	settings, err := config.Map()
	if err != nil {
		return nil, rpc.NewError(rpc.CodeInternal, err.Error(),
			"fix or remove "+config.Path())
	}
	// A leftover `decide:` block from the retired judgment layer may hold an API
	// key. Nothing reads it any more, so it is not sent out either.
	delete(settings, "decide")
	return rpc.ConfigGetResult{Config: settings}, nil
}

func handleConfigSet(_ context.Context, req *Request) (any, error) {
	var p rpc.ConfigSetParams
	err := req.Bind(&p)
	if err != nil {
		return nil, err
	}
	if len(p.Patch) == 0 {
		return nil, rpc.NewError(rpc.CodeInvalidParams, "patch is required",
			`send {"patch": {"list": {"sort": "port"}}}; a null value clears a key`)
	}
	settings, err := config.Apply(p.Patch)
	if err != nil {
		return nil, rpc.NewError(rpc.CodeInvalidParams, err.Error(),
			"the config was left as it was; `oberth config path` shows the file")
	}
	req.HostContext.Logger.Info("config written", "path", config.Path())
	return rpc.ConfigSetResult{OK: true, Config: settings}, nil
}

func handleConfigPath(_ context.Context, _ *Request) (any, error) {
	path := config.Path()
	return rpc.ConfigPathResult{Path: path}, nil
}

// republish makes a write visible before its own reply is. The last scan is
// re-attributed and published synchronously, so by the time the handler returns
// the delta carrying the change is queued on every subscriber's connection —
// ahead of this call's response, which the same writer queues afterwards — and
// the caller's own next read comes from a snapshot that already has it.
//
// Contract §18 asks only that a rename or an assign take effect "in the next
// publish", with an immediate rescan so the caller sees it. Replying *after*
// that publish is what turns "the next delta carries the rename" from a race
// into a guarantee: a subscriber cannot receive the acknowledgement ahead of
// the delta that justifies it, and a client reading straight after the reply
// cannot be served state from before its own write.
//
// The machine is not scanned. A rename, a group pin and a `oberth.yaml` edit
// change how ports the runtime service already knows are named and grouped, not which
// ports exist, so re-running attribution over the last scan's rows answers the
// question in microseconds instead of behind `lsof`, `ps` and `docker stats`.
// `Republish` wakes the loop, so a real scan follows.
func republish(rt *HostContext) {
	err := rt.Scanner.Republish()
	if err != nil {
		rt.Logger.Warn("republishing after a write", "error", err)
	}
}

// rescanAfterKill is republish's sibling for the one write that really does
// change which ports exist. A kill must be followed by a real scan: the ports
// that just went away leave the snapshot only once the OS has been asked
// again, and their port_down rows reach the history ring from that delta.
func rescanAfterKill(rt *HostContext) (state.Snapshot, error) {
	snapshot, err := rt.Scanner.Rescan(scanner.Include{})
	if err != nil {
		rt.Logger.Warn("rescanning after a kill", "error", err)
	}
	return snapshot, err
}

// storeError wraps a database failure as an internal error naming the path.
func storeError(operation string, cause error) error {
	if errors.Is(cause, store.ErrInvalidName) {
		return rpc.NewError(rpc.CodeInvalidParams, cause.Error(),
			"names are a single word: no whitespace, no / and no \\")
	}
	return rpc.NewError(rpc.CodeInternal, operation+": "+cause.Error(),
		"check `oberth runtime service log`")
}
