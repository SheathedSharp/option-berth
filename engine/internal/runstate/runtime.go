// Package runstate implements Oberth Runtime's pure, generation-bound policies.
// Runtime Service supplies captured facts; this package never observes the OS.
package runstate

import (
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/servicefacts"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"path/filepath"
	"slices"
)

// Project refreshes known declarations from the authoritative registry capture.
// It never invents listener freshness or changes its input snapshot.
func Project(previous state.Snapshot, facts servicefacts.Snapshot, listenersFresh bool) state.Snapshot {
	out := previous
	out.Groups = slices.Clone(previous.Groups)
	listeners := previous.Ports
	if !listenersFresh {
		listeners = nil
		out.Ports = []state.Port{}
		out.Observation = &state.Observation{State: "partial", Code: "observation_incomplete", Source: "runtime_registry", Detail: "run facts are current; listener observation requires refresh"}
	}
	resolver := servicefacts.NewResolver(facts, listeners)
	for i, g := range out.Groups {
		g.Services = slices.Clone(g.Services)
		for j, row := range g.Services {
			cwd := row.Cwd
			if g.RootDir != nil && !filepath.IsAbs(cwd) {
				cwd = filepath.Join(*g.RootDir, cwd)
			}
			g.Services[j] = groups.ApplyServiceFacts(row, resolver.Resolve(servicefacts.Declaration{Key: servicefacts.Key{Group: g.Name, Service: row.Name}, Port: value(row.Port), PortAuto: row.PortAuto}), cwd)
		}
		if !listenersFresh {
			g.Members = []int{}
			g.Machine = slices.Clone(g.Machine)
			for j := range g.Machine {
				g.Machine[j].Listening = false
				g.Machine[j].ListeningKnown = false
			}
		}
		g.Status = groups.RuntimeStatus(g)
		out.Groups[i] = g
	}
	groups.FinalizeResources(out.Groups)
	return out
}
func value(p *int) int {
	if p != nil {
		return *p
	}
	return 0
}
