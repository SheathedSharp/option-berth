package state

// LocalhostName is the reserved name used on rows observed by this daemon.
const LocalhostName = "localhost"

// PrefixKey keeps local keys stable while accepting rows that carry an
// optional host label. This label remains on row types for wire compatibility;
// this daemon publishes only its own rows.
func PrefixKey(host, key string) string {
	if IsLocalhost(host) {
		return key
	}
	return host + "/" + key
}

// IsLocalhost reports whether a row belongs to this daemon. Empty is accepted
// for rows produced by older clients and direct scans.
func IsLocalhost(host string) bool {
	return host == "" || host == LocalhostName
}

// HostOf normalizes the optional row label for key construction.
func HostOf(host string) string {
	if host == "" {
		return LocalhostName
	}
	return host
}
