package ports

// serviceHint is one well-known port and its IANA service name. The table is
// only a display hint for a port whose owning process cannot be identified
// (an unprivileged scan cannot see another user's sockets).
type serviceHint struct {
	port int
	name string
}

var serviceHints = []serviceHint{
	{20, "ftp-data"},
	{21, "ftp"},
	{22, "ssh"},
	{23, "telnet"},
	{25, "smtp"},
	{53, "dns"},
	{67, "dhcp"},
	{68, "dhcp"},
	{69, "tftp"},
	{80, "http"},
	{110, "pop3"},
	{111, "rpcbind"},
	{119, "nntp"},
	{123, "ntp"},
	{135, "msrpc"},
	{139, "netbios-ssn"},
	{143, "imap"},
	{161, "snmp"},
	{179, "bgp"},
	{389, "ldap"},
	{443, "https"},
	{445, "smb"},
	{465, "smtps"},
	{514, "syslog"},
	{515, "printer"},
	{587, "smtp-submission"},
	{631, "ipp"},
	{636, "ldaps"},
	{873, "rsync"},
	{993, "imaps"},
	{995, "pop3s"},
	{1080, "socks"},
	{1194, "openvpn"},
	{1433, "mssql"},
	{1521, "oracle"},
	{2049, "nfs"},
	{2375, "docker"},
	{2376, "docker"},
	{3306, "mysql"},
	{3389, "rdp"},
	{5353, "mdns"},
	{5432, "postgresql"},
	{5672, "amqp"},
	{5900, "vnc"},
	{6379, "redis"},
	{6443, "kube-apiserver"},
	{9200, "elasticsearch"},
	{11211, "memcached"},
	{27017, "mongodb"},
}

// wellKnownServices is the mutable lookup RegisterServices extends.
var wellKnownServices = buildServiceTable(serviceHints)

func buildServiceTable(hints []serviceHint) map[int]string {
	table := make(map[int]string, len(hints))
	for _, hint := range hints {
		table[hint.port] = hint.name
	}
	return table
}

// ServiceName is the well-known service name for a port, or "" when the port
// is not in the table.
func ServiceName(port int) string {
	return wellKnownServices[port]
}

// RegisterServices merges user port labels over the built-in table; an empty
// label is ignored.
func RegisterServices(labels map[int]string) {
	for port, label := range labels {
		if label == "" {
			continue
		}
		wellKnownServices[port] = label
	}
}
