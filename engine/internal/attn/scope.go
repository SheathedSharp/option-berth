package attn

// Select limits a derived artifact to explicit task targets. Identities and the
// source revision remain unchanged so the existing freshness/CAS checks apply.
// A named service follows replacement runs; a run target never does.
func Select(a Artifact, services, runs []string) (Artifact, bool) {
	unscoped := len(services) == 0 && len(runs) == 0
	serviceSet, runSet := map[string]bool{}, map[string]bool{}
	for _, s := range services {
		serviceSet[s] = true
	}
	for _, r := range runs {
		runSet[r] = true
	}
	events := a.Events
	if len(events) == 0 {
		events = []Event{a.Event}
	}
	selected := make([]Event, 0, len(events))
	for _, e := range events {
		if (!e.RunOnly && (unscoped || serviceSet[e.Service])) || (e.RunID != "" && runSet[e.RunID]) {
			selected = append(selected, e)
		}
	}
	if len(selected) == 0 {
		return Artifact{}, false
	}
	evidence := make([]Evidence, 0, len(a.Evidence))
	for _, e := range a.Evidence {
		for _, event := range selected {
			if e.Service == event.Service && e.RunID == event.RunID {
				evidence = append(evidence, e)
				break
			}
		}
	}
	a.Event, a.Events, a.Evidence, a.Options = selected[0], selected, evidence, optionsForAll(selected)
	return a, true
}
