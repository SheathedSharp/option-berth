package ports

import (
	"fmt"
	"strings"
)

// FindByPort scans and returns the listener on a port, filtered to bindIP when
// one is given. Several binds of the same port without a bindIP is an error
// that lists them, because guessing would attribute the wrong process.
func FindByPort(port int, bindIP string) (*ListeningPort, error) {
	scanRows, err := Scan()
	if err != nil {
		return nil, err
	}

	hits := FindAllByPort(port, scanRows)
	if len(hits) == 0 {
		return nil, fmt.Errorf("no process found listening on port %d", port)
	}
	if bindIP != "" {
		return pickBind(hits, bindIP, port)
	}
	if len(hits) == 1 {
		return &hits[0], nil
	}
	return nil, ambiguousBinds(port, hits)
}

// pickBind chooses the match on one bind address.
func pickBind(matches []ListeningPort, bindIP string, port int) (*ListeningPort, error) {
	for i := range matches {
		if matches[i].BindAddress == bindIP {
			return &matches[i], nil
		}
	}
	return nil, fmt.Errorf("no process found listening on %s:%d", bindIP, port)
}

// ambiguousBinds reports the addresses a caller must choose between.
func ambiguousBinds(port int, matches []ListeningPort) error {
	addresses := make([]string, len(matches))
	for i := range matches {
		addresses[i] = matches[i].BindAddress
	}
	return fmt.Errorf(
		"port %d is bound to multiple addresses: %s\nUse --ip to specify which one (e.g. --ip %s)",
		port, strings.Join(addresses, ", "), addresses[0])
}
