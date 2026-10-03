package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// This file is the `share:` block of the user config: where the relay is.
//
// One setting today, because one is all the session half of sharing needs. It
// is a setting rather than a constant because a self-hosted relay is a stated
// goal (option-berth-relay/docs/AUTH.md, open question 3) and because the tests of
// the device flow point it at an httptest server.
//
// **There is no default relay.** There used to be one — the upstream project's
// host — and it was removed on 2026-09-24: a default pointing at somebody
// else's server is worse than no default, because a sign-in flow would use it
// without anyone ever choosing it. An empty origin means "no relay is
// configured", and the share code refuses rather than guessing.

// RelayEnv overrides share.relay for one process, the way BERTH_SOCKET
// overrides the socket. It is how a test points the daemon at a fake relay and
// how someone tries a self-hosted one without editing a file.
const RelayEnv = "BERTH_RELAY"

// TunnelEnv overrides share.tunnel for one process, the way RelayEnv overrides
// share.relay.
const TunnelEnv = "BERTH_RELAY_TUNNEL"

// ShareConfig is the `share:` block of the user config.
type ShareConfig struct {
	// Relay is the relay's base origin. Empty means no relay is configured.
	Relay string `yaml:"relay"`
	// Tunnel is where a share's control connection is dialled, when that is
	// not the relay itself. Empty — which is what every hosted install uses —
	// means Relay, because one hostname behind one proxy serves both.
	//
	// It exists because the share edge and the control plane are not promised
	// to be the same process: option-berth-relay/docs/SHARE.md names "a second box
	// for the share edge" as a live option and says the code should not assume
	// otherwise, and a relay run locally already splits them across two ports.
	// One setting is cheaper than discovering that assumption later.
	Tunnel string `yaml:"tunnel"`
}

// Relay returns the relay origin this machine uses: $BERTH_RELAY, then
// `share.relay`, then nothing. The result has no trailing slash, so a route can
// always be appended verbatim.
//
// A value that is not an absolute http(s) URL is ignored with a warning rather
// than failing: the relay is not something a mistyped config file should be
// able to take the daemon down over.
func (c *Config) Relay() string {
	relay, _ := ResolveRelay(os.Getenv(RelayEnv), c.Share.Relay)
	return relay
}

// Tunnel returns where a share's control connection is dialled: $BERTH_RELAY_TUNNEL,
// then `share.tunnel`, then whatever Relay returns.
func (c *Config) Tunnel() string {
	tunnel, _ := ResolveRelay(os.Getenv(TunnelEnv), c.Share.Tunnel)
	if strings.TrimSpace(os.Getenv(TunnelEnv)) == "" && strings.TrimSpace(c.Share.Tunnel) == "" {
		return c.Relay()
	}
	return tunnel
}

// ResolveRelay is Relay with both inputs given, so the precedence and the
// validation can be tested without touching the environment. It returns the
// origin — empty when nothing is configured — and a warning naming anything it
// ignored.
func ResolveRelay(env, configured string) (string, string) {
	if v := strings.TrimSpace(env); v != "" {
		if origin, err := normalizeRelay(v); err == nil {
			return origin, ""
		} else {
			return "", fmt.Sprintf("config: ignoring %s=%q: %v", RelayEnv, v, err)
		}
	}
	if v := strings.TrimSpace(configured); v != "" {
		if origin, err := normalizeRelay(v); err == nil {
			return origin, ""
		} else {
			return "", fmt.Sprintf("config: ignoring share.relay %q: %v — no relay is configured", v, err)
		}
	}
	return "", ""
}

// Origin reduces a relay URL written any reasonable way to the origin
// [Config.Relay] would return, so two spellings of one relay compare equal.
// `session.register` uses it to tell "the app is pointed somewhere else" from
// "the app wrote a trailing slash".
func Origin(raw string) (string, error) { return normalizeRelay(strings.TrimSpace(raw)) }

// normalizeRelay accepts an absolute http or https URL and returns it with any
// trailing slash and any path, query or fragment removed: the relay is an
// origin, and `https://relay.example/v1/` with a route appended would be
// `https://relay.example/v1//v1/device/code`.
func normalizeRelay(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("not a URL: %w", err)
	}
	switch u.Scheme {
	case "http", "https":
	case "":
		return "", fmt.Errorf("no scheme: write it as https://%s", raw)
	default:
		return "", fmt.Errorf("scheme %q is not http or https", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("no host")
	}
	return u.Scheme + "://" + u.Host, nil
}
