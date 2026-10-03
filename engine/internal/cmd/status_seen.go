package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/internal/git"
	"github.com/sheathedsharp/option-berth/internal/paths"
	"github.com/sheathedsharp/option-berth/internal/state"
)

// This file is what makes the second `oberth status` different from the
// first: the mark of what it last showed, and the comparison against it.
//
// It is deliberately the same shape the app keeps in seen.json (a reader who
// understands one understands the other), in a file of its own — see
// paths.SeenCLI for why the two readers do not share an anchor.

// statusSeenVersion is the file's shape version. A future change bumps it, and
// an older file is then ignored rather than misread.
const statusSeenVersion = 1

// maxSeenAge and maxSeenFiles mirror the app's ledger: a mark nobody has looked
// at in a month is noise, and a repository with more than five hundred changed
// files is not one this command's one-line comparison can summarise anyway.
const (
	maxSeenAge   = 30 * 24 * time.Hour
	maxSeenFiles = 500
)

// statusSeen is the ledger: one entry per repository root.
type statusSeen struct {
	Version int                        `json:"version"`
	Roots   map[string]statusSeenEntry `json:"roots"`
}

// statusSeenEntry is what one root looked like when it was last shown.
type statusSeenEntry struct {
	// At is when this state was first seen. It does not move while the state
	// does not: "上次看 14:20 · 从那以后没变" reads as the time the look
	// started, which is the honest one.
	At       string           `json:"at"`
	Head     string           `json:"head"`
	Files    []statusSeenFile `json:"files"`
	Services []statusSeenSvc  `json:"services"`
	Ports    []int            `json:"ports"`
}

type statusSeenFile struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	Additions *int   `json:"additions,omitempty"`
	Deletions *int   `json:"deletions,omitempty"`
}

type statusSeenSvc struct {
	Name    string `json:"name"`
	Running bool   `json:"running"`
	Port    int    `json:"port,omitempty"`
}

// statusChange is the difference the second run reports. It is a list of facts
// and nothing else: no adjectives, no verdict about whether any of it is good
// (decision 0013).
type statusChange struct {
	// At is when the mark this is measured from was taken.
	At              string           `json:"at"`
	Empty           bool             `json:"empty"`
	ServicesStarted []string         `json:"services_started,omitempty"`
	ServicesStopped []string         `json:"services_stopped,omitempty"`
	PortsOpened     []int            `json:"ports_opened,omitempty"`
	PortsClosed     []int            `json:"ports_closed,omitempty"`
	Files           statusFileChange `json:"files"`
	HeadFrom        string           `json:"head_from,omitempty"`
	HeadTo          string           `json:"head_to,omitempty"`
}

type statusFileChange struct {
	Appeared []string `json:"appeared,omitempty"`
	Touched  []string `json:"touched,omitempty"`
	Gone     []string `json:"gone,omitempty"`
}

// statusSeenFrom is the entry a run records: the facts the comparison above
// needs, and nothing else. It is deliberately not "everything the command
// printed" — a ledger that grows with the output would make the file a second
// copy of the state.
func statusSeenFrom(root string, group state.Group, ports []state.Port, tree git.Tree, at time.Time) statusSeenEntry {
	entry := statusSeenEntry{At: at.UTC().Format(time.RFC3339), Head: tree.Head}

	for _, f := range tree.Files {
		entry.Files = append(entry.Files, statusSeenFile{
			Path: f.Path, Status: f.Status, Additions: f.Additions, Deletions: f.Deletions,
		})
		if len(entry.Files) >= maxSeenFiles {
			break
		}
	}
	sort.Slice(entry.Files, func(i, j int) bool { return entry.Files[i].Path < entry.Files[j].Path })

	for _, s := range group.Services {
		svc := statusSeenSvc{Name: s.Name, Running: s.Running}
		if s.PortActual != nil {
			svc.Port = *s.PortActual
		} else if s.Port != nil {
			svc.Port = *s.Port
		}
		entry.Services = append(entry.Services, svc)
	}
	sort.Slice(entry.Services, func(i, j int) bool { return entry.Services[i].Name < entry.Services[j].Name })

	for _, p := range ports {
		entry.Ports = append(entry.Ports, p.Port)
	}
	sort.Ints(entry.Ports)

	return entry
}

// compareStatusSeen is the whole of J6: what is different now from what the
// mark recorded.
//
// One rule, applied everywhere: a thing that was not there and is, appeared; a
// thing that was and is not, is gone; a thing that is there and changed, is
// touched. Nothing is judged.
func compareStatusSeen(before, now statusSeenEntry) statusChange {
	change := statusChange{At: before.At}

	beforeSvc := map[string]statusSeenSvc{}
	for _, s := range before.Services {
		beforeSvc[s.Name] = s
	}
	nowSvc := map[string]statusSeenSvc{}
	for _, s := range now.Services {
		nowSvc[s.Name] = s
	}
	for _, s := range now.Services {
		if old, ok := beforeSvc[s.Name]; !ok || (!old.Running && s.Running) {
			change.ServicesStarted = append(change.ServicesStarted, s.Name)
		}
	}
	for _, s := range before.Services {
		was := beforeSvc[s.Name]
		is, ok := nowSvc[s.Name]
		if !was.Running {
			continue
		}
		if !ok || !is.Running {
			change.ServicesStopped = append(change.ServicesStopped, s.Name)
		}
	}

	beforePorts := map[int]bool{}
	for _, p := range before.Ports {
		beforePorts[p] = true
	}
	nowPorts := map[int]bool{}
	for _, p := range now.Ports {
		nowPorts[p] = true
	}
	for _, p := range now.Ports {
		if !beforePorts[p] {
			change.PortsOpened = append(change.PortsOpened, p)
		}
	}
	for _, p := range before.Ports {
		if !nowPorts[p] {
			change.PortsClosed = append(change.PortsClosed, p)
		}
	}

	beforeFiles := map[string]statusSeenFile{}
	for _, f := range before.Files {
		beforeFiles[f.Path] = f
	}
	nowFiles := map[string]statusSeenFile{}
	for _, f := range now.Files {
		nowFiles[f.Path] = f
	}
	for _, f := range now.Files {
		old, ok := beforeFiles[f.Path]
		if !ok {
			change.Files.Appeared = append(change.Files.Appeared, f.Path)
			continue
		}
		if old.Status != f.Status || !sameCount(old.Additions, f.Additions) ||
			!sameCount(old.Deletions, f.Deletions) {
			change.Files.Touched = append(change.Files.Touched, f.Path)
		}
	}
	for _, f := range before.Files {
		if _, ok := nowFiles[f.Path]; !ok {
			change.Files.Gone = append(change.Files.Gone, f.Path)
		}
	}

	if before.Head != "" && now.Head != "" && before.Head != now.Head {
		change.HeadFrom, change.HeadTo = shortHash(before.Head), shortHash(now.Head)
	}

	change.Empty = len(change.ServicesStarted) == 0 && len(change.ServicesStopped) == 0 &&
		len(change.PortsOpened) == 0 && len(change.PortsClosed) == 0 &&
		len(change.Files.Appeared) == 0 && len(change.Files.Touched) == 0 &&
		len(change.Files.Gone) == 0 && change.HeadFrom == ""
	return change
}

func sameCount(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func shortHash(h string) string {
	if len(h) > 7 {
		return h[:7]
	}
	return h
}

// loadStatusSeen reads the ledger. A file that is missing, unreadable or from a
// future version reads as an empty one: the comparison is context, and a
// broken ledger must never be the reason a command fails.
func loadStatusSeen() statusSeen {
	doc := statusSeen{Version: statusSeenVersion, Roots: map[string]statusSeenEntry{}}
	data, err := os.ReadFile(paths.SeenCLI())
	if err != nil {
		return doc
	}
	var read statusSeen
	if err := json.Unmarshal(data, &read); err != nil || read.Version != statusSeenVersion {
		return doc
	}
	if read.Roots != nil {
		doc.Roots = read.Roots
	}
	return doc
}

// mark records what this run showed, and reports the entry that was there
// before it (so the caller can compare against the previous look, not against
// itself).
//
// A state identical to the last one does not move the mark: the time in `at` is
// when this state was first seen, which is what makes "从那以后没变" true.
// A write that fails is not an error — the answer is already in hand, and the
// only thing lost is the next comparison.
func (d *statusSeen) mark(root string, now statusSeenEntry) (before statusSeenEntry, had bool) {
	before, had = d.Roots[root]
	if had && sameSeenState(before, now) {
		return before, true
	}
	cutoff := time.Now().Add(-maxSeenAge)
	for key, entry := range d.Roots {
		if at, err := time.Parse(time.RFC3339, entry.At); err != nil || at.Before(cutoff) {
			delete(d.Roots, key)
		}
	}
	d.Roots[root] = now
	d.Version = statusSeenVersion
	_ = d.save()
	return before, had
}

// sameSeenState is what decides whether the mark moves. `at` is not part of it,
// for the reason above.
func sameSeenState(a, b statusSeenEntry) bool {
	if a.Head != b.Head || len(a.Files) != len(b.Files) ||
		len(a.Services) != len(b.Services) || len(a.Ports) != len(b.Ports) {
		return false
	}
	for i := range a.Files {
		x, y := a.Files[i], b.Files[i]
		if x.Path != y.Path || x.Status != y.Status ||
			!sameCount(x.Additions, y.Additions) || !sameCount(x.Deletions, y.Deletions) {
			return false
		}
	}
	for i := range a.Services {
		if a.Services[i] != b.Services[i] {
			return false
		}
	}
	for i := range a.Ports {
		if a.Ports[i] != b.Ports[i] {
			return false
		}
	}
	return true
}

// save writes the ledger atomically: a half-written one would read as "no
// baseline", which silently turns the next comparison into a first look.
func (d statusSeen) save() error {
	path := paths.SeenCLI()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".seen-*")
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return os.Rename(tmp.Name(), path)
}

// seenSummary is the one human line for a comparison, in the app's voice —
// 「上次看 14:20 · api 起来了 · 多了 2 个文件 · HEAD 从 a 到了 b」.
//
// Only the kinds of change that actually happened get a clause: a line that
// listed the empty ones would be a template, not an answer.
func seenSummary(change statusChange, at time.Time) string {
	when := "上次看 " + at.Local().Format("15:04")
	if change.Empty {
		return when + " · 从那以后没变"
	}
	parts := make([]string, 0, 7)
	add := func(s string) {
		if s != "" {
			parts = append(parts, s)
		}
	}
	add(namedPart(change.ServicesStarted, "起来了", "个服务"))
	add(namedPart(change.ServicesStopped, "停了", "个服务"))
	add(countedPart(change.PortsOpened, "新听", "个端口"))
	add(countedPart(change.PortsClosed, "不再听", "个端口"))
	add(namedPart(change.Files.Appeared, "多了", "个文件"))
	add(namedPart(change.Files.Touched, "又改了", "个文件"))
	add(namedPart(change.Files.Gone, "落定了", "个文件"))
	if change.HeadFrom != "" {
		add("HEAD 从 " + change.HeadFrom + " 到了 " + change.HeadTo)
	}
	if len(parts) == 0 {
		return when + " · 从那以后没变"
	}
	return when + " · " + strings.Join(parts, " · ")
}

// namedPart names up to two things, then counts the rest — the app's own rule
// (GitPanel.names), kept so the two readers sound alike.
func namedPart(names []string, verb, unit string) string {
	if len(names) == 0 {
		return ""
	}
	short := names
	if len(short) > 2 {
		short = short[:2]
	}
	if len(names) > len(short) {
		return strings.Join(short, "\u3001") + " 等 " + fmt.Sprint(len(names)) + unit + verb
	}
	return strings.Join(short, "\u3001") + " " + verb
}

// countedPart is the same for numbers, where the verb reads better first —
// 「新听 8080」, not 「8080 新听」.
func countedPart(values []int, verb, unit string) string {
	if len(values) == 0 {
		return ""
	}
	names := make([]string, 0, len(values))
	for _, v := range values {
		names = append(names, fmt.Sprint(v))
	}
	if len(names) > 2 {
		return verb + " " + strings.Join(names[:2], "\u3001") + " 等 " + fmt.Sprint(len(names)) + unit
	}
	return verb + " " + strings.Join(names, "\u3001")
}
