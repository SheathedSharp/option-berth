package state

import (
	"strconv"
	"strings"
)

// The other half of PrefixKey: taking a delta key apart again. Clients can
// hand keys from the stream back as selectors without each handler learning
// the grammar.

// SplitHostPrefix takes a `"<host>/<rest>"` key apart. The prefix is only read
// as a host when known says so, which keeps a group whose name happens to
// contain a slash from being mistaken for a labeled row: callers provide the
// known names, so the parser never has to guess.
//
// A key with no recognised prefix comes back unchanged with an empty host,
// which is the localhost case and the overwhelmingly common one.
func SplitHostPrefix(key string, known func(string) bool) (host, rest string) {
	name, after, found := strings.Cut(key, "/")
	if !found || name == "" || after == "" {
		return "", key
	}
	if name == LocalhostName {
		return LocalhostName, after
	}
	if known != nil && known(name) {
		return name, after
	}
	return "", key
}

// ParsePortKey reads the `"<port>"` or `"<port>:<bind_address>"` half of a port
// key. The bind address may itself contain colons (`"::1"`, `"::"`), so the
// split is at the first one only.
func ParsePortKey(key string) (port int, bind string, ok bool) {
	key = strings.TrimSpace(key)
	if key == "" {
		return 0, "", false
	}
	head, tail, found := strings.Cut(key, ":")
	n, err := strconv.Atoi(head)
	if err != nil || n <= 0 || n > 65535 {
		return 0, "", false
	}
	if !found {
		return n, "", true
	}
	return n, tail, true
}
