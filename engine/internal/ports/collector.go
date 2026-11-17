package ports

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
)

// Capabilities is what a collector's table can be trusted for. It is the fact a
// reader needs before concluding that a port is free, and the source of the
// sentence published with a table read by anything other than the platform's
// primary collector.
type Capabilities struct {
	// SeesOtherUsers means the table covers sockets owned by other users. A
	// collector without it reads this user's sockets only, so a port held by a
	// root process can be missing from it.
	SeesOtherUsers bool
	// DualStackWildcard means a dual-stack wildcard socket is reported under
	// both address families. Without it a Go/Node/Java default bind appears
	// under one family only, and a client connecting through the other is
	// looking at a port the table never showed.
	DualStackWildcard bool
	// AttributesPID means a row can name the process that owns the socket. A
	// collector without it still says a port is listening, but not who holds it.
	AttributesPID bool
}

// limits is one clause per capability this collector does not have, in the order
// they change what a reader may conclude. A primary collector's limits are
// descriptive only; an alternative collector's are published.
func (c Capabilities) limits() []string {
	var out []string
	if !c.SeesOtherUsers {
		out = append(out, "cannot see sockets owned by other users")
	}
	if !c.DualStackWildcard {
		out = append(out, "reports a dual-stack wildcard socket under one address family only")
	}
	if !c.AttributesPID {
		out = append(out, "cannot name the process that owns every socket")
	}
	return out
}

// Collector is one rung of a platform's listener chain: a named way to read the
// listener table, the coverage it can promise, and the read itself, which
// reaches the OS only through the listenerCommand seam.
type Collector struct {
	// Name is what observation.source publishes when this collector answers and
	// it is not the platform's primary one. For every shipped collector it is
	// the command that answered.
	Name string
	// Capabilities is what this collector's table covers.
	Capabilities Capabilities
	// collect reads the table. Readable rows travel with the error when the pass
	// could not be trusted, so the caller can publish them together with the
	// verdict instead of choosing between a partial table and none.
	collect func(run listenerCommand) ([]ListeningPort, error)
}

// What each shipped collector can promise. The primary collector of a platform
// is the table this product treats as authoritative, so its capabilities are
// descriptive. Only an alternative's are ever published, and each one declares
// at least one limit: a table that could see everything is what the primary is
// for, and naming another collector never turns absence into a fact.
var (
	// Darwin netstat reads the TCP PCB table: every owner's sockets, one row per
	// dual-stack socket (tcp46), and the owning pid.
	darwinNetstatCapabilities = Capabilities{SeesOtherUsers: true, DualStackWildcard: true, AttributesPID: true}
	// lsof walks the file table instead of reading the PCB. A non-root lsof
	// cannot see other users' sockets, and it reports a dual-stack wildcard
	// socket under one family (docs/cli.md, "Listener observation").
	darwinLsofCapabilities = Capabilities{SeesOtherUsers: false, DualStackWildcard: false, AttributesPID: true}
	// ss reads the kernel's socket table and shows a dual-stack wildcard socket
	// once, under its IPv6 family.
	ssCapabilities = Capabilities{SeesOtherUsers: true, DualStackWildcard: false, AttributesPID: true}
	// Linux netstat reads the same kernel table as ss, in the older format.
	linuxNetstatCapabilities = Capabilities{SeesOtherUsers: true, DualStackWildcard: false, AttributesPID: true}
	// Windows netstat -ano lists every owner's sockets with the owning pid, and
	// prints a dual-stack wildcard socket once, under its IPv6 family.
	windowsNetstatCapabilities = Capabilities{SeesOtherUsers: true, DualStackWildcard: false, AttributesPID: true}
)

// chain is the ordered collectors a platform may read its listener table with.
// The first is the primary: its table is the one this product treats as
// authoritative, it is never named in observation.source, and its absence is
// what the platform's own tools report. The rest are alternatives, tried in
// order when the primary could not answer. A platform with a single rung has no
// alternative and never publishes a source.
func chain(platform string) []Collector {
	switch platform {
	case "darwin":
		return []Collector{
			// -l is required: Darwin otherwise truncates numeric IPv6 addresses
			// to 16 characters, even with -n/-v/-W. Never parse a shortened identity.
			tableCollector("netstat", darwinNetstatCapabilities, []string{"-anvl", "-p", "tcp"}, darwinNetstatListeners),
			// macOS 27: a child of a parent Apple has not signed runs netstat
			// successfully and still gets no TCP table (issue #156). lsof reads
			// the same sockets by walking the file table, which that denial does
			// not cover.
			lsofCollector(),
		}
	case "linux":
		return []Collector{
			tableCollector("ss", ssCapabilities, []string{"-tlnp"}, ssListeners),
			// The rung for a minimal image or a host without iproute2, where ss
			// is simply not installed. Both commands read the same kernel table.
			tableCollector("netstat", linuxNetstatCapabilities, []string{"-tlnp"}, linuxNetstatListeners),
		}
	case "windows":
		return []Collector{
			tableCollector("netstat", windowsNetstatCapabilities, []string{"-ano"}, netstatListeners),
		}
	}
	return nil
}

// tableCollector is a rung that runs one fixed command and decodes its table.
func tableCollector(tool string, caps Capabilities, args []string, format listenerFormat) Collector {
	return Collector{
		Name:         tool,
		Capabilities: caps,
		collect: func(run listenerCommand) ([]ListeningPort, error) {
			return collectTable(run, tool, args, format)
		},
	}
}

// collectTable runs one collector command and classifies what came back.
//
// Every collector here prints at least a header, even when nothing is listening.
// A sandbox can let the command exit 0 while denying the syscall behind the
// table, which leaves stdout empty — that is an observation that never happened,
// not an empty machine.
func collectTable(run listenerCommand, tool string, args []string, format listenerFormat) ([]ListeningPort, error) {
	stdout, stderr, err := run(tool, args...)
	detail := strings.TrimSpace(string(stderr))

	if err == nil {
		if len(bytes.TrimSpace(stdout)) == 0 {
			return nil, observationf(CodeObservationEmpty, observationEmptyHint,
				"%s: exited 0 but printed no listening table%s", tool, observationDetailSuffix(detail))
		}
		if detail != "" {
			// A header or a row netstat could not read leaves a table the caller
			// must not publish as a complete one.
			return nil, observationf(CodeObservationIncomplete, observationIncompleteHint,
				"%s: incomplete listener observation: %s", tool, firstObservationLine(detail))
		}
		return decodeTable(string(stdout), format)
	}

	// A non-zero exit is a failed command. With diagnostics it is an observation
	// the caller can classify; with no output at all there is nothing to explain
	// and it stays an ordinary error.
	if detail == "" {
		detail = strings.TrimSpace(string(stdout))
	}
	if detail == "" {
		return nil, fmt.Errorf("%s: %w", tool, err)
	}
	return nil, observationErr(err, CodeObservationIncomplete, observationIncompleteHint,
		"%s: incomplete listener observation: %v\n%s", tool, err, firstObservationLine(detail))
}

// decodeTable turns one collector's printed table into rows. Only the Darwin
// netstat table has a decoder that can reject the whole pass; the other formats
// carry their own header and rows, and an unreadable row is dropped where it
// stands rather than invalidating every row after it.
func decodeTable(text string, format listenerFormat) ([]ListeningPort, error) {
	if format == darwinNetstatListeners {
		return decodeDarwinNetstat(text)
	}
	return decodeListeners(text, format), nil
}

// lsofCollector is Darwin's alternative rung: the same sockets read by walking
// the file table. It is not the primary because that walk is narrower than the
// PCB (see darwinLsofCapabilities).
func lsofCollector() Collector {
	return Collector{Name: listenerSourceLsofName, Capabilities: darwinLsofCapabilities, collect: collectLsof}
}

// collectLsof runs the alternative and reports an empty match as an empty
// table, never as a healthy one: two collectors that both printed nothing are
// still not proof of an idle machine, and the caller decides what the silence
// means.
func collectLsof(run listenerCommand) ([]ListeningPort, error) {
	args := []string{"-a", "-n", "-P", "-w", "+c", "0", "-iTCP", "-sTCP:LISTEN", "-Fpcltn"}
	stdout, stderr, err := run(listenerSourceLsofName, args...)
	// lsof exits 1 when nothing matches, so a silent non-zero exit with no
	// output is an empty table rather than a broken command.
	var exited interface{ ExitCode() int }
	emptyMatch := errors.As(err, &exited) && exited.ExitCode() == 1 &&
		len(bytes.TrimSpace(stdout)) == 0 && len(bytes.TrimSpace(stderr)) == 0
	switch {
	case err == nil, emptyMatch:
		rows, decodeErr := decodeDarwinLsof(string(stdout))
		if decodeErr != nil {
			return nil, decodeErr
		}
		if len(rows) == 0 {
			return nil, observationf(CodeObservationEmpty, observationEmptyHint,
				"lsof: printed no listening table (exit %s)", lsofExit(err))
		}
		return rows, nil
	case len(bytes.TrimSpace(stderr)) > 0:
		return nil, observationErr(err, CodeObservationIncomplete, observationIncompleteHint,
			"lsof: incomplete listener observation: %v\n%s", err, firstObservationLine(strings.TrimSpace(string(stderr))))
	default:
		return nil, fmt.Errorf("lsof: %w", err)
	}
}

// lsofExit names how the alternative ended, for the sentence that reports every
// collector. A nil error is a clean exit; anything else keeps its own message.
func lsofExit(err error) string {
	if err == nil {
		return "0"
	}
	return err.Error()
}

// sourceCapabilities finds the collector a published source names on the given
// platform. An unknown name has no declared capabilities, and a caller must not
// read that as "covers everything": only the platform's primary table is read
// that way, and the primary is never named.
func sourceCapabilities(platform, source string) (Capabilities, bool) {
	for _, c := range chain(platform) {
		if c.Name == source {
			return c.Capabilities, true
		}
	}
	return Capabilities{}, false
}

// narrowSourceDetail is NarrowSourceDetail for an explicit platform, so the
// derivation is testable on a machine of any OS.
func narrowSourceDetail(platform, source string) string {
	if source == "" {
		return ""
	}
	if caps, ok := sourceCapabilities(platform, source); ok {
		if limits := caps.limits(); len(limits) > 0 {
			return "the listener table was read with " + source + ", which " + joinLimits(limits) +
				"; a port missing from it is unknown, not free"
		}
	}
	// A name this build does not know still cannot turn absence into a fact.
	return "the listener table was read with " + source + "; a port missing from it is unknown, not free"
}

// joinLimits reads as one English clause list, so the published sentence stays a
// sentence however many capabilities a collector is missing.
func joinLimits(limits []string) string {
	switch len(limits) {
	case 0:
		return ""
	case 1:
		return limits[0]
	}
	return strings.Join(limits[:len(limits)-1], ", ") + " and " + limits[len(limits)-1]
}
