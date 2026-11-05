package ports

import "testing"

func TestServiceName(t *testing.T) {
	cases := []struct {
		port int
		name string
	}{
		{53, "dns"},
		{631, "ipp"},
		{443, "https"},
		{3306, "mysql"},
		{4100, ""},  // not a well-known port
		{99999, ""}, // out of range
	}
	for _, tc := range cases {
		if got := ServiceName(tc.port); got != tc.name {
			t.Errorf("ServiceName(%d) = %q, want %q", tc.port, got, tc.name)
		}
	}
}

func TestRegisterServices(t *testing.T) {
	// Snapshot and restore so other tests see the built-in table.
	saved := make(map[int]string, len(wellKnownServices))
	for port, name := range wellKnownServices {
		saved[port] = name
	}
	t.Cleanup(func() { wellKnownServices = saved })

	RegisterServices(map[int]string{
		9100: "print-agent", // new port
		53:   "internal-dns",
		2468: "", // empty label — skipped
	})

	if got := ServiceName(9100); got != "print-agent" {
		t.Errorf("ServiceName(9100) = %q, want print-agent", got)
	}
	if got := ServiceName(53); got != "internal-dns" {
		t.Errorf("ServiceName(53) = %q, want the override", got)
	}
	if got := ServiceName(2468); got != "" {
		t.Errorf("ServiceName(2468) = %q, want empty", got)
	}
}
