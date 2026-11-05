package state

import (
	"strconv"
	"strings"
)

// This file is the inverse of PrefixKey: it takes a delta key apart again, so
// a client can feed keys from the stream back as selectors without learning
// the grammar.

// SplitHostPrefix separates a `"<host>/<rest>"` key. A leading segment counts
// as a host only when it is localhost or known says so, which keeps a group
// whose name contains a slash from being read as a labeled row. Anything else
// is returned whole with an empty host — the localhost case.
func SplitHostPrefix(key string, known func(string) bool) (host, rest string) {
	name, tail, hasSlash := strings.Cut(key, "/")
	switch {
	case !hasSlash || name == "" || tail == "":
		return "", key
	case name == LocalhostName:
		return LocalhostName, tail
	case known != nil && known(name):
		return name, tail
	}
	return "", key
}

// ParsePortKey reads the `"<port>"` or `"<port>:<bind_address>"` half of a port
// key. A bind address may contain colons itself (`"::1"`, `"::"`), so only the
// first colon separates it.
func ParsePortKey(key string) (port int, bind string, ok bool) {
	key = strings.TrimSpace(key)
	head, tail, hasBind := strings.Cut(key, ":")
	number, err := strconv.Atoi(head)
	if err != nil || number < 1 || number > 65535 {
		return 0, "", false
	}
	if !hasBind {
		return number, "", true
	}
	return number, tail, true
}
