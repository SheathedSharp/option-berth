package feedback

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestLedgerRoundTripIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feedback", "reports.ndjson")
	now := time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	entry := Entry{
		At: now.Format(time.RFC3339), Fingerprint: "abc123", Kind: "slow",
		Command: "up", Version: "v2.0.0", Channel: "gh",
		URL: "https://github.com/x/y/issues/1", Note: true,
	}
	if err := AppendLedger(path, entry); err != nil {
		t.Fatalf("AppendLedger: %v", err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("ledger mode = %o, want 600", mode)
		}
	}
	entries, err := ReadLedger(path)
	if err != nil {
		t.Fatalf("ReadLedger: %v", err)
	}
	if len(entries) != 1 || entries[0] != entry {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestMissingLedgerIsEmpty(t *testing.T) {
	entries, err := ReadLedger(filepath.Join(t.TempDir(), "absent.ndjson"))
	if err != nil || entries != nil {
		t.Fatalf("ReadLedger = (%v, %v), want nothing", entries, err)
	}
}

func TestLedgerSkipsAHalfWrittenLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reports.ndjson")
	body := `{"at":"2026-11-06T09:00:00Z","fingerprint":"a","kind":"slow","command":"up","channel":"gh"}` + "\n" +
		`{"at":"2026-11-06T09:00:01Z","finger` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := ReadLedger(path)
	if err != nil {
		t.Fatalf("ReadLedger: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want the one complete line", entries)
	}
}

func TestDuplicateWindowAndDailyCount(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	entries := []Entry{
		{At: now.Add(-2 * time.Hour).Format(time.RFC3339), Fingerprint: "old", URL: "https://x/1"},
		{At: now.Add(-26 * time.Hour).Format(time.RFC3339), Fingerprint: "stale", URL: "https://x/2"},
	}

	if got, ok := DuplicateSince(entries, "old", now, DuplicateWindow); !ok || got.URL != "https://x/1" {
		t.Fatalf("duplicate in window = (%+v, %v)", got, ok)
	}
	if _, ok := DuplicateSince(entries, "stale", now, DuplicateWindow); ok {
		t.Fatal("an entry older than the window is not a duplicate")
	}
	if _, ok := DuplicateSince(entries, "unseen", now, DuplicateWindow); ok {
		t.Fatal("an unseen fingerprint is not a duplicate")
	}
	if count := CountSince(entries, now, DuplicateWindow); count != 1 {
		t.Fatalf("CountSince = %d, want 1", count)
	}
}
