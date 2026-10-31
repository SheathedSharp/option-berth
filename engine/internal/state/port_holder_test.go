package state

import "testing"

// TestServiceExitEqualityComparesPortHolderByValue: the reducer allocates a
// fresh holder on every publication, so pointer identity would report a change
// on every scan. Two content-equal holders must be equal; any changed scalar
// must not be.
func TestServiceExitEqualityComparesPortHolderByValue(t *testing.T) {
	a := ServiceExit{
		Code: 1, Reason: "port_occupied", At: "2026-10-30T12:00:00Z", RunID: "run-1",
		PortHint: 3000,
		PortHolder: &PortHolder{
			Port: 3000, BindAddress: "127.0.0.1", PID: 7, Command: "node server.js",
			Cwd: "/work/api", Kind: PortHolderRun, Group: "shop@other", Name: "api", RunID: "run-9",
		},
	}
	holder := *a.PortHolder
	b := a
	b.PortHolder = &holder
	if !sameExit(&a, &b) {
		t.Fatal("content-equal holders compared unequal")
	}
	if !serviceEqual(&Service{Name: "api", LastExit: &a}, &Service{Name: "api", LastExit: &b}) {
		t.Fatal("service rows with content-equal holders compared unequal")
	}

	for name, mutate := range map[string]func(*PortHolder){
		"port":         func(h *PortHolder) { h.Port = 3001 },
		"bind_address": func(h *PortHolder) { h.BindAddress = "0.0.0.0" },
		"pid":          func(h *PortHolder) { h.PID = 8 },
		"command":      func(h *PortHolder) { h.Command = "other" },
		"cwd":          func(h *PortHolder) { h.Cwd = "/elsewhere" },
		"kind":         func(h *PortHolder) { h.Kind = PortHolderUnknown },
		"group":        func(h *PortHolder) { h.Group = "shop" },
		"name":         func(h *PortHolder) { h.Name = "web" },
		"run_id":       func(h *PortHolder) { h.RunID = "run-10" },
	} {
		t.Run(name, func(t *testing.T) {
			changedHolder := *b.PortHolder
			changed := b
			changed.PortHolder = &changedHolder
			mutate(changed.PortHolder)
			if sameExit(&a, &changed) {
				t.Fatalf("a changed holder.%s compared equal", name)
			}
		})
	}

	if sameExit(nil, &a) || sameExit(&a, nil) {
		t.Fatal("a nil exit must not equal a recorded one")
	}
}

// TestServiceExitPortHintIsPartOfTheFact: the port a run was told to bind
// survives a manifest edit and must participate in change detection.
func TestServiceExitPortHintIsPartOfTheFact(t *testing.T) {
	a := ServiceExit{Reason: "port_occupied", PortHint: 3000}
	b := a
	b.PortHint = 3001
	if sameExit(&a, &b) {
		t.Fatal("a changed port hint compared equal")
	}
}
