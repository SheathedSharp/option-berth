package feedback

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	// DuplicateWindow is how long the same complaint from the same build is
	// remembered. A repeated sighting is common; a repeated issue is noise.
	DuplicateWindow = 24 * time.Hour
	// DailyLimit bounds one installation to this many filed reports in a
	// rolling day, so a loop in an agent cannot turn into a flood.
	DailyLimit = 5
	// LedgerShow is how many entries `oberth feedback log` prints.
	LedgerShow = 20
)

// Entry is one report this installation produced. It is an activity record,
// not runtime state: nothing reads it to decide whether a service is running.
type Entry struct {
	At          string `json:"at"`
	Fingerprint string `json:"fingerprint"`
	Kind        string `json:"kind"`
	Command     string `json:"command"`
	Version     string `json:"version,omitempty"`
	Channel     string `json:"channel"`
	URL         string `json:"url,omitempty"`
	Note        bool   `json:"note,omitempty"`
}

// ReadLedger reads the append-only ledger. A missing file is no entries, and a
// malformed line is skipped: the ledger is advisory, and a half-written line
// must not make every later report fail.
func ReadLedger(path string) ([]Entry, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()

	var entries []Entry
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024)
	for scanner.Scan() {
		var entry Entry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return entries, err
	}
	return entries, nil
}

// AppendLedger adds one entry, owner-only.
func AppendLedger(path string, entry Entry) error {
	if entry.At == "" {
		entry.At = time.Now().UTC().Format(time.RFC3339)
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encoding the report entry: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if _, err := file.Write(append(line, '\n')); err != nil {
		file.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return file.Close()
}

// DuplicateSince returns the matching entry when the same fingerprint was
// filed inside window.
func DuplicateSince(entries []Entry, fingerprint string, now time.Time, window time.Duration) (Entry, bool) {
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Fingerprint != fingerprint {
			continue
		}
		at, err := time.Parse(time.RFC3339, entries[i].At)
		if err != nil {
			continue
		}
		if now.Sub(at) <= window {
			return entries[i], true
		}
	}
	return Entry{}, false
}

// CountSince counts the reports filed inside window.
func CountSince(entries []Entry, now time.Time, window time.Duration) int {
	count := 0
	for _, entry := range entries {
		at, err := time.Parse(time.RFC3339, entry.At)
		if err != nil {
			continue
		}
		// A clock that stepped backwards must not let an entry count against
		// the limit twice or never; anything at or before now inside the
		// window counts once.
		if !at.After(now) && now.Sub(at) <= window {
			count++
		}
	}
	return count
}
