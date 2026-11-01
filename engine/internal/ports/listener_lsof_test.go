package ports

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

// The fixture keeps lsof's real field-record layout (`-Fpcln`: pid, command,
// descriptor, lock, endpoint) for the address shapes macOS prints: the
// wildcard in both families, a loopback pair, and an IPv6 literal with a zone.
func TestDarwinLsofFixtureDecodesEveryAddressShape(t *testing.T) {
	data, err := os.ReadFile("testdata/darwin_lsof_listen.txt")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := decodeDarwinLsof(string(data))
	if err != nil {
		t.Fatalf("fixture rejected: %v", err)
	}
	want := []ListeningPort{
		{Port: 18123, PID: 900, Process: "relayd", BindAddress: "::", IPVersion: "IPv6"},
		{Port: 18123, PID: 900, Process: "relayd", BindAddress: "0.0.0.0", IPVersion: "IPv4"},
		{Port: 18124, PID: 901, Process: "node", BindAddress: "::", IPVersion: "IPv6"},
		{Port: 18124, PID: 901, Process: "node", BindAddress: "0.0.0.0", IPVersion: "IPv4"},
		{Port: 18125, PID: 902, Process: "java", BindAddress: "127.0.0.1", IPVersion: "IPv4"},
		{Port: 18125, PID: 902, Process: "java", BindAddress: "::1", IPVersion: "IPv6"},
		{Port: 18126, PID: 903, Process: "postgres", BindAddress: "fe80::1%lo0", IPVersion: "IPv6"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows = %+v\nwant   %+v", rows, want)
	}
}

// A dual-stack socket is two lsof records and one service. Folding them is what
// keeps status, readiness and dependency waits from counting it twice.
func TestDarwinLsofDualStackFoldsToOneRow(t *testing.T) {
	text := "p900\ncrelayd\nf3\ntIPv6\nn*:18123\nf4\ntIPv4\nn*:18123\n"
	rows, err := decodeDarwinLsof(text)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	merged := mergeAddresses(rows)
	if len(merged) != 1 {
		t.Fatalf("merged = %+v, want one listener for one socket", merged)
	}
	if merged[0].PID != 900 || merged[0].Process != "relayd" || merged[0].Port != 18123 {
		t.Fatalf("merged row = %+v", merged[0])
	}
	if !reflect.DeepEqual(merged[0].BindAddresses, []string{"0.0.0.0"}) {
		t.Fatalf("secondary binds = %v, want the IPv4 half", merged[0].BindAddresses)
	}
}

func TestDarwinLsofRejectsRowsWithoutAFamily(t *testing.T) {
	// A wildcard endpoint without its t record cannot be placed in 0.0.0.0 or
	// ::. Guessing would publish a wrong identity, so the row is a problem.
	text := "p900\ncrelayd\nf3\nn*:18123\n"
	rows, err := decodeDarwinLsof(text)
	if len(rows) != 0 {
		t.Fatalf("rows = %+v, want none", rows)
	}
	var oe *ObservationError
	if !errors.As(err, &oe) || oe.Code != CodeObservationIncomplete || !oe.Partial {
		t.Fatalf("error = %+v, want a partial observation_incomplete", err)
	}
}

func TestDarwinLsofKeepsReadableRowsWhenOneRowFails(t *testing.T) {
	text := "p900\ncrelayd\nf3\ntIPv4\nn127.0.0.1:18123\n" +
		"p901\ncnode\nf4\ntIPv4\nnnot-an-endpoint\n"
	rows, err := decodeDarwinLsof(text)
	if len(rows) != 1 || rows[0].Port != 18123 {
		t.Fatalf("rows = %+v, want the readable row", rows)
	}
	partial, ok := PartialRows(err)
	if !ok || len(partial) != 1 || partial[0].Port != 18123 {
		t.Fatalf("PartialRows = %+v, %v", partial, ok)
	}
	var oe *ObservationError
	if !errors.As(err, &oe) || oe.Code != CodeObservationIncomplete || !oe.Partial {
		t.Fatalf("error = %+v", err)
	}
	if !strings.Contains(oe.Error(), "record 10") {
		t.Fatalf("detail = %q, want the offending record number", oe.Error())
	}
}

// lsof exits 1 with no output when nothing matches. That is an empty machine,
// not a collector failure, and the decoder must not turn it into one.
func TestDarwinLsofEmptyTableIsAnEmptyObservation(t *testing.T) {
	rows, err := decodeDarwinLsof("")
	if err != nil || len(rows) != 0 {
		t.Fatalf("rows=%+v err=%v, want an empty healthy table", rows, err)
	}
}

func TestDarwinLsofUnrecognizedOutputNamesTheReason(t *testing.T) {
	var oe *ObservationError
	if !errors.As(mustDecodeLsof(t, "lsof: not installed\n"), &oe) || oe.Code != CodeObservationUnrecognized {
		t.Fatalf("error = %v, want %q", mustDecodeLsof(t, "lsof: not installed\n"), CodeObservationUnrecognized)
	}
}

func mustDecodeLsof(t *testing.T, text string) error {
	t.Helper()
	_, err := decodeDarwinLsof(text)
	return err
}

// The fallback runs only when the primary collector printed nothing at all. A
// table that was printed and could not be decoded is a decoder problem, and
// switching tools would hide it.
func TestDarwinLsofFallbackRunsOnlyForAnEmptyNetstatTable(t *testing.T) {
	calls := 0
	run := func(name string, _ ...string) ([]byte, []byte, error) {
		calls++
		if name != "lsof" {
			return nil, nil, nil
		}
		return []byte("p900\ncrelayd\nf3\ntIPv4\nn127.0.0.1:18123\n"), nil, nil
	}
	rows, source, err := collectListenersSource("darwin", run)
	if err != nil || calls != 2 {
		t.Fatalf("rows=%+v source=%q err=%v calls=%d", rows, source, err, calls)
	}
	if source != listenerSourceLsofName || len(rows) != 1 || rows[0].Port != 18123 {
		t.Fatalf("source=%q rows=%+v, want the lsof table", source, rows)
	}

	calls = 0
	run = func(name string, _ ...string) ([]byte, []byte, error) {
		calls++
		return []byte(darwinTCPHeader + "tcp4 0\n"), nil, nil
	}
	if _, _, err := collectListenersSource("darwin", run); err == nil || calls != 1 {
		t.Fatalf("undecodable netstat table got %d calls, error=%v; want one call and a decoder error", calls, err)
	}
}

// A sandbox that denies netstat may deny lsof too. The primary keeps its code
// so the reader still learns what was refused, and the fallback's reason rides
// in the same sentence instead of replacing it.
func TestDarwinLsofFallbackKeepsThePrimaryReasonWhenBothFail(t *testing.T) {
	run := func(name string, _ ...string) ([]byte, []byte, error) {
		if name == "lsof" {
			return nil, []byte("lsof: no permission\n"), collectorExit(1)
		}
		return nil, nil, nil
	}
	rows, source, err := collectListenersSource("darwin", run)
	if len(rows) != 0 || source != "" {
		t.Fatalf("rows=%+v source=%q, want nothing published", rows, source)
	}
	var oe *ObservationError
	if !errors.As(err, &oe) || oe.Code != CodeObservationEmpty {
		t.Fatalf("error = %+v, want the primary's %q", err, CodeObservationEmpty)
	}
	if !strings.Contains(oe.Error(), "no listening table") || !strings.Contains(oe.Error(), "no permission") {
		t.Fatalf("detail = %q, want both collectors named", oe.Error())
	}
}

// An empty lsof match exits 1 with nothing on either stream. That is lsof
// saying "nothing matched", but two silent collectors are still not proof of an
// idle machine, so the round stays failed and the self-probe decides.
func TestDarwinLsofEmptyMatchStaysASuspiciousEmptyTable(t *testing.T) {
	run := func(name string, _ ...string) ([]byte, []byte, error) {
		if name == "lsof" {
			return nil, nil, collectorExit(1)
		}
		return nil, nil, nil
	}
	rows, source, err := collectListenersSource("darwin", run)
	if len(rows) != 0 || source != "" {
		t.Fatalf("rows=%+v source=%q, want nothing published", rows, source)
	}
	var oe *ObservationError
	if !errors.As(err, &oe) || oe.Code != CodeObservationEmpty || oe.Partial {
		t.Fatalf("error = %+v, want the primary's non-partial %q", err, CodeObservationEmpty)
	}
	if !strings.Contains(oe.Error(), "lsof: printed no listening table") {
		t.Fatalf("detail = %q, want lsof's silent table named", oe.Error())
	}
}

func TestDarwinLsofFallbackIsNotUsedOnOtherPlatforms(t *testing.T) {
	calls := 0
	run := func(name string, _ ...string) ([]byte, []byte, error) {
		calls++
		if name == "lsof" {
			t.Fatal("a non-darwin platform must not fall back to lsof")
		}
		return nil, nil, nil
	}
	if _, _, err := collectListenersSource("linux", run); err == nil || calls != 1 {
		t.Fatalf("calls=%d err=%v, want one ss call and its own failure", calls, err)
	}
}

// The probe verdict is unchanged by which collector answered — it only ever
// asks about a socket this process owns — but the detail must say so, because a
// reader deciding that some *other* port is absent needs the narrower table.
func TestProbeNamesTheFallbackCollector(t *testing.T) {
	got := probeListenerObservationSource(context.Background(),
		func(_ context.Context, port, pid int) ([]ListeningPort, string, error) {
			return []ListeningPort{{Port: port, PID: pid, BindAddress: "127.0.0.1"}}, listenerSourceLsofName, nil
		})
	if got.State != ObservationObservable || got.Source != listenerSourceLsofName {
		t.Fatalf("probe = %+v", got)
	}
	if !strings.Contains(got.Detail, "via lsof") {
		t.Fatalf("detail = %q, want the fallback collector named", got.Detail)
	}
}

func FuzzDarwinLsofDecoder(f *testing.F) {
	f.Add("p900\ncrelayd\nf3\ntIPv4\nn127.0.0.1:18123\n")
	f.Fuzz(func(t *testing.T, text string) {
		seen := map[listenerKey]bool{}
		rows, _ := decodeDarwinLsof(text)
		for _, row := range rows {
			if row.Port <= 0 || row.Port > 65535 || row.PID < 0 {
				t.Fatalf("invalid fact: %+v", row)
			}
			key := listenerKey{row.Port, row.BindAddress}
			if seen[key] {
				t.Fatalf("duplicate: %+v", key)
			}
			seen[key] = true
		}
	})
}
