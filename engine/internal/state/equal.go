package state

import "slices"

// Typed comparisons keep the wire model out of reflection and interface
// boxing on every scan. Field-wise oracle tests guard against omitted fields
// when the wire structs grow; nested comparable records fail to compile if a
// non-comparable field is added. No hash/fingerprint can hide a state change.
func portsEqual(a, b *Port, withStats bool) bool {
	return a.Host == b.Host && a.Port == b.Port && a.BindAddress == b.BindAddress &&
		sliceEqual(a.BindAddresses, b.BindAddresses) &&
		a.IPVersion == b.IPVersion && a.URL == b.URL && a.PID == b.PID && a.PPID == b.PPID &&
		a.Process == b.Process && a.DisplayName == b.DisplayName && pointerEqual(a.Name, b.Name) &&
		a.Command == b.Command && a.Cwd == b.Cwd && a.CwdInTrash == b.CwdInTrash && a.CwdGone == b.CwdGone &&
		pointerEqual(a.ProjectRoot, b.ProjectRoot) && pointerEqual(a.Group, b.Group) &&
		pointerEqual(a.GroupSource, b.GroupSource) && pointerEqual(a.Session, b.Session) &&
		a.Type == b.Type && a.User == b.User && pointerEqual(a.Run, b.Run) &&
		(!withStats || pointerEqual(a.Stats, b.Stats)) && healthEqual(a.Health, b.Health) &&
		pointerEqual(a.Docker, b.Docker) && pointerEqual(a.StartedAt, b.StartedAt)
}

func groupsEqual(a, b Group) bool {
	// Host/Name define Key, not the old group's payload comparison. In
	// particular an empty host and localhost share the same public group key.
	return a.Status == b.Status && a.Repo == b.Repo && a.Worktree == b.Worktree &&
		a.Branch == b.Branch && a.Source == b.Source &&
		pointerEqual(a.RootDir, b.RootDir) && pointerEqual(a.ConfigPath, b.ConfigPath) &&
		sliceEqual(a.Members, b.Members) && servicesEqual(a.Services, b.Services) &&
		machineEqual(a.Machine, b.Machine)
}

func servicesEqual(a, b []Service) bool {
	// Existing service semantics deliberately equate nil and empty lists.
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !serviceEqual(&a[i], &b[i]) {
			return false
		}
	}
	return true
}

func serviceEqual(a, b *Service) bool {
	return a.Name == b.Name && a.Prepare == b.Prepare && a.Cmd == b.Cmd && a.Cwd == b.Cwd &&
		pointerEqual(a.Port, b.Port) && a.PortAuto == b.PortAuto && pointerEqual(a.Health, b.Health) &&
		healthEqual(a.HealthStatus, b.HealthStatus) &&
		pointerEqual(a.RuntimeCmd, b.RuntimeCmd) && pointerEqual(a.RuntimeCwd, b.RuntimeCwd) &&
		pointerEqual(a.ManifestHash, b.ManifestHash) && pointerEqual(a.RuntimeSpecHash, b.RuntimeSpecHash) &&
		a.ManifestRuntimeMismatch == b.ManifestRuntimeMismatch && pointerEqual(a.Description, b.Description) &&
		pointerEqual(a.Icon, b.Icon) && pointerEqual(a.Color, b.Color) && sliceEqual(a.DependsOn, b.DependsOn) &&
		a.Running == b.Running && pointerEqual(a.PortActual, b.PortActual) && pointerEqual(a.PID, b.PID) &&
		pointerEqual(a.RunID, b.RunID) && pointerEqual(a.StartedAt, b.StartedAt) &&
		pointerEqual(a.LogPath, b.LogPath) && pointerEqual(a.LastExit, b.LastExit)
}

func machineEqual(a, b []MachineRef) bool {
	if (a == nil) != (b == nil) || len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || a[i].Port != b[i].Port || a[i].Listening != b[i].Listening ||
			!pointerEqual(a[i].Unit, b[i].Unit) {
			return false
		}
	}
	return true
}

func pointerEqual[T comparable](a, b *T) bool {
	// Pointer identity matters for NaN too: reflect.DeepEqual treats the same
	// pointer as equal, but two distinct NaN-bearing values as unequal.
	return a == b || (a != nil && b != nil && *a == *b)
}

func sliceEqual[T comparable](a, b []T) bool {
	// Unlike slices.Equal alone, preserve nil versus a non-nil empty slice.
	return (a == nil) == (b == nil) && slices.Equal(a, b)
}
