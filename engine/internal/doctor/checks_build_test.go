package doctor

import (
	"context"
	"errors"
	"testing"
)

func TestDoctorBuildIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, cli, remote, want string
		reachable               bool
		err                     error
	}{
		{"same version different commit", "aaaaaaa", "bbbbbbb", StatusWarn, true, nil},
		{"matching", "aaaaaaa", "aaaaaaa", StatusOK, true, nil},
		{"missing daemon identity", "aaaaaaa", "", StatusWarn, true, nil},
		{"unknown on both sides", "unknown", "unknown", StatusWarn, true, nil},
		{"local build", "local", "local", StatusWarn, true, nil},
		{"probe failed", "aaaaaaa", "aaaaaaa", StatusWarn, true, errors.New("fixture RPC failed")},
		{"offline", "aaaaaaa", "", StatusSkip, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := fakeEnv(t)
			env.Commit = tc.cli
			calls := 0
			env.Daemon = func(context.Context) DaemonInfo {
				calls++
				return DaemonInfo{Reachable: tc.reachable, Version: env.Version, Commit: tc.remote, StatusErr: tc.err}
			}
			got := Run(context.Background(), *env, []string{"daemon_build_matches"})
			if len(got.Checks) != 1 {
				t.Fatalf("missing build check: %+v", got)
			}
			wantStatus(t, got.Checks[0], tc.want)
			if calls != 1 {
				t.Fatalf("probe count=%d", calls)
			}
			if got.Checks[0].Fixable {
				t.Fatal("a read-only diagnosis must not authorize restarting services")
			}
		})
	}
}

func TestBuildCheckSkipsDaemonMode(t *testing.T) {
	env := fakeEnv(t)
	env.Mode = ModeDaemon
	result := Run(context.Background(), *env, []string{"daemon_build_matches"})
	if len(result.Checks) != 1 {
		t.Fatal(result)
	}
	wantStatus(t, result.Checks[0], StatusSkip)
}
