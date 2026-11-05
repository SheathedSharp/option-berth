package state

import "testing"

func TestSplitHostPrefixReadsOnlyKnownHosts(t *testing.T) {
	known := func(name string) bool { return name == "hetzner" }
	cases := []struct {
		key    string
		host   string
		rest   string
		lookup func(string) bool
	}{
		{"3000:127.0.0.1", "", "3000:127.0.0.1", known},
		{"hetzner/3000:127.0.0.1", "hetzner", "3000:127.0.0.1", known},
		{"localhost/3000:127.0.0.1", "localhost", "3000:127.0.0.1", known},
		// An unregistered prefix is part of the name, not a host: a group may
		// legitimately be called that.
		{"nope/3000", "", "nope/3000", known},
		{"my/group", "", "my/group", known},
		{"hetzner/api", "hetzner", "api", known},
		// With no known hosts, only localhost can be a host.
		{"hetzner/3000", "", "hetzner/3000", nil},
		// A missing side leaves the key whole.
		{"/3000", "", "/3000", known},
		{"hetzner/", "", "hetzner/", known},
		{"", "", "", known},
	}
	for _, tc := range cases {
		host, rest := SplitHostPrefix(tc.key, tc.lookup)
		if host != tc.host || rest != tc.rest {
			t.Errorf("SplitHostPrefix(%q) = (%q, %q), want (%q, %q)",
				tc.key, host, rest, tc.host, tc.rest)
		}
	}
}

func TestParsePortKeySplitsAtTheFirstColonOnly(t *testing.T) {
	cases := []struct {
		key  string
		port int
		bind string
		ok   bool
	}{
		{"3000", 3000, "", true},
		{"3000:127.0.0.1", 3000, "127.0.0.1", true},
		{"3000:0.0.0.0", 3000, "0.0.0.0", true},
		// An IPv6 bind address is full of colons.
		{"8080:::1", 8080, "::1", true},
		{"8080:::", 8080, "::", true},
		{"8080:*", 8080, "*", true},
		// Rejected forms.
		{"", 0, "", false},
		{"api", 0, "", false},
		{"0:127.0.0.1", 0, "", false},
		{"70000", 0, "", false},
	}
	for _, tc := range cases {
		port, bind, ok := ParsePortKey(tc.key)
		if port != tc.port || bind != tc.bind || ok != tc.ok {
			t.Errorf("ParsePortKey(%q) = (%d, %q, %v), want (%d, %q, %v)",
				tc.key, port, bind, ok, tc.port, tc.bind, tc.ok)
		}
	}
}

// The two parsers must undo PrefixKey exactly, for every host spelling.
func TestPortKeyRoundTripsThroughBothParsers(t *testing.T) {
	known := func(name string) bool { return name == "hetzner" }
	for _, host := range []string{"", LocalhostName, "hetzner"} {
		row := Port{Host: host, Port: 3000, BindAddress: "::1"}
		parsedHost, rest := SplitHostPrefix(row.Key(), known)
		if HostOf(parsedHost) != HostOf(host) {
			t.Errorf("host of %q = %q, want %q", row.Key(), parsedHost, host)
		}
		port, bind, ok := ParsePortKey(rest)
		if !ok || port != 3000 || bind != "::1" {
			t.Errorf("ParsePortKey(%q) = (%d, %q, %v)", rest, port, bind, ok)
		}
	}
}
