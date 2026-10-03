package attention

// Preflight is an optional adapter handoff before an agent's proposed side
// effect. Its schema distinguishes agent intent from code-produced runtime
// events, and it never becomes a daemon request or an executable command.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const PreflightSchema = "oberth.attention.preflight/v1"
const PreflightTTL = 2 * time.Minute
const StatusMaxAge = 30 * time.Second

// Intent is supplied by the agent, not the model. Permission is a context
// description, not an authorization token. The consuming agent must still
// confirm the person's scope before executing any action.
type Intent struct {
	Action         string `json:"action"`
	Service        string `json:"service,omitempty"`
	DraftGroup     string `json:"draft_group,omitempty"`
	UserPermission string `json:"user_permission"`
	UserApproved   bool   `json:"user_approved"`
}

type StatusSnapshot struct {
	Scope struct {
		Root string `json:"root"`
	} `json:"scope"`
	Worktree struct {
		Name       string            `json:"name"`
		Branch     string            `json:"branch"`
		RootDir    *string           `json:"root_dir"`
		ConfigPath *string           `json:"config_path"`
		Services   []SnapshotService `json:"services"`
	} `json:"worktree"`
	Drafts []SnapshotDraft `json:"drafts"`
}

// SnapshotService intentionally excludes command, environment and log text.
type SnapshotService struct {
	Name       string  `json:"name"`
	Running    bool    `json:"running"`
	RunID      *string `json:"run_id"`
	PID        *int    `json:"pid"`
	Port       *int    `json:"port"`
	PortActual *int    `json:"port_actual"`
}

type SnapshotDraft struct {
	Group string `json:"group"`
	Path  string `json:"path"`
	Root  string `json:"root"`
	At    string `json:"at,omitempty"`
}

type PreflightTarget struct {
	Project    string   `json:"project"`
	Service    string   `json:"service,omitempty"`
	Services   []string `json:"services,omitempty"`
	DraftGroup string   `json:"draft_group,omitempty"`
	DraftPath  string   `json:"draft_path,omitempty"`
	DraftHash  string   `json:"draft_sha256,omitempty"`
}

type Preflight struct {
	Schema        string          `json:"schema"`
	RequestID     string          `json:"request_id"`
	WorktreeRoot  string          `json:"worktree_root"`
	Branch        string          `json:"branch,omitempty"`
	StateRevision string          `json:"state_revision"`
	GeneratedAt   string          `json:"generated_at"`
	FreshUntil    string          `json:"fresh_until"`
	Intent        Intent          `json:"intent"`
	Target        PreflightTarget `json:"target"`
	Evidence      []Evidence      `json:"evidence"`
	Options       []Option        `json:"options"`
	Assessment    *Assessment     `json:"assessment,omitempty"`
}

// ReadStatusSnapshot accepts only a freshly captured status file. The adapter
// does not run the CLI, scan a process, or treat an old saved snapshot as live.
func ReadStatusSnapshot(path string, now time.Time) (StatusSnapshot, error) {
	info, err := os.Stat(path)
	if err != nil {
		return StatusSnapshot{}, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if !info.Mode().IsRegular() || now.Sub(info.ModTime()) > StatusMaxAge || info.ModTime().After(now.Add(time.Second)) {
		return StatusSnapshot{}, errors.New("attention: status snapshot is not fresh; capture oberth status --json again")
	}
	file, err := os.Open(path)
	if err != nil {
		return StatusSnapshot{}, err
	}
	defer file.Close()
	var snapshot StatusSnapshot
	decoder := json.NewDecoder(io.LimitReader(file, 2<<20))
	if err := decoder.Decode(&snapshot); err != nil {
		return StatusSnapshot{}, fmt.Errorf("attention: decode status snapshot: %w", err)
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return StatusSnapshot{}, errors.New("attention: status must contain exactly one JSON value")
	}
	if snapshot.Scope.Root == "" || !filepath.IsAbs(snapshot.Scope.Root) || snapshot.Worktree.Name == "" {
		return StatusSnapshot{}, errors.New("attention: status snapshot lacks worktree identity")
	}
	if snapshot.Worktree.RootDir != nil && filepath.Clean(*snapshot.Worktree.RootDir) != filepath.Clean(snapshot.Scope.Root) {
		return StatusSnapshot{}, errors.New("attention: status snapshot worktree roots disagree")
	}
	return snapshot, nil
}

// DerivePreflight binds an agent's fixed action intent to existing status
// evidence. home is used only to verify draft provenance, never as an action
// argument. No arbitrary action path or shell parameter is accepted.
func DerivePreflight(snapshot StatusSnapshot, intent Intent, home string, now time.Time) (Preflight, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if intent.UserPermission == "" || len(intent.UserPermission) > maxTaskBytes {
		return Preflight{}, errors.New("attention: explicit user_permission context is required (up to 2000 bytes)")
	}
	intent.UserPermission = redactText(intent.UserPermission, maxTaskBytes)
	if !KnownOptionID(intent.Action) {
		return Preflight{}, fmt.Errorf("attention: unknown preflight action %q", intent.Action)
	}
	root := filepath.Clean(snapshot.Scope.Root)
	if !filepath.IsAbs(root) || snapshot.Worktree.Name == "" {
		return Preflight{}, errors.New("attention: invalid status worktree")
	}
	target := PreflightTarget{Project: snapshot.Worktree.Name}
	refs := []Evidence{{Ref: "status.scope.root"}, {Ref: "status.worktree"}}
	var options []Option
	switch intent.Action {
	case "start_service", "restart_service":
		if intent.Service == "" || intent.DraftGroup != "" {
			return Preflight{}, errors.New("attention: start/restart requires one declared service and no draft")
		}
		found := false
		for _, service := range snapshot.Worktree.Services {
			if service.Name == intent.Service {
				found = true
				break
			}
		}
		if !found {
			return Preflight{}, errors.New("attention: target is not a declared worktree service")
		}
		target.Service = intent.Service
		refs = append(refs, Evidence{Ref: "status.worktree.services"})
		options = []Option{{ID: intent.Action}, {ID: "inspect_manifest"}, {ID: "continue_without_action"}, {ID: "ask_user_for_context"}}
	case "stop_service":
		if intent.Service != "" || intent.DraftGroup != "" {
			return Preflight{}, errors.New("attention: stop_service is project-wide; service/draft selectors are unsupported")
		}
		for _, service := range snapshot.Worktree.Services {
			target.Services = append(target.Services, service.Name)
		}
		if len(target.Services) == 0 {
			return Preflight{}, errors.New("attention: project has no declared services to stop")
		}
		sort.Strings(target.Services)
		options = []Option{{ID: intent.Action}, {ID: "continue_without_action"}, {ID: "ask_user_for_context"}}
	case "adopt_manifest":
		if strings.TrimSpace(home) == "" {
			return Preflight{}, errors.New("attention: adopt_manifest requires BERTH_HOME")
		}
		if intent.Service != "" || intent.DraftGroup == "" {
			return Preflight{}, errors.New("attention: adopt_manifest requires a draft_group from current status")
		}
		for _, draft := range snapshot.Drafts {
			if draft.Group == intent.DraftGroup && filepath.Clean(draft.Root) == root {
				if !filepath.IsAbs(draft.Path) {
					return Preflight{}, errors.New("attention: draft path must be absolute")
				}
				if filepath.Clean(filepath.Dir(draft.Path)) != filepath.Join(filepath.Clean(home), "drafts") {
					return Preflight{}, errors.New("attention: draft path is outside BERTH_HOME/drafts")
				}
				sum, err := draftChecksum(draft.Path)
				if err != nil {
					return Preflight{}, err
				}
				target.DraftGroup, target.DraftPath, target.DraftHash = draft.Group, draft.Path, sum
				break
			}
		}
		if target.DraftHash == "" {
			return Preflight{}, errors.New("attention: selected draft has no current worktree evidence")
		}
		refs = append(refs, Evidence{Ref: "status.drafts"})
		options = []Option{{ID: intent.Action}, {ID: "inspect_manifest"}, {ID: "continue_without_action"}, {ID: "ask_user_for_context"}}
	default:
		return Preflight{}, errors.New("attention: preflight action must be start_service, restart_service, stop_service, or adopt_manifest")
	}
	revision, err := snapshotRevision(snapshot)
	if err != nil {
		return Preflight{}, err
	}
	identity, _ := json.Marshal(struct {
		Revision string
		Intent   Intent
		Target   PreflightTarget
	}{revision, intent, target})
	sum := sha256.Sum256(identity)
	return Preflight{Schema: PreflightSchema, RequestID: hex.EncodeToString(sum[:])[:24], WorktreeRoot: root, Branch: snapshot.Worktree.Branch, StateRevision: revision, GeneratedAt: now.Format(time.RFC3339Nano), FreshUntil: now.Add(PreflightTTL).Format(time.RFC3339Nano), Intent: intent, Target: target, Evidence: refs, Options: options}, nil
}

func snapshotRevision(snapshot StatusSnapshot) (string, error) {
	copy := snapshot
	copy.Worktree.Services = append([]SnapshotService(nil), snapshot.Worktree.Services...)
	copy.Drafts = append([]SnapshotDraft(nil), snapshot.Drafts...)
	sort.Slice(copy.Worktree.Services, func(i, j int) bool { return copy.Worktree.Services[i].Name < copy.Worktree.Services[j].Name })
	sort.Slice(copy.Drafts, func(i, j int) bool { return copy.Drafts[i].Group < copy.Drafts[j].Group })
	data, err := json.Marshal(copy)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:24], nil
}

func draftChecksum(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return "", errors.New("attention: draft must be a regular file up to 1 MiB")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func PreflightPath(home string, request Preflight) string {
	return filepath.Join(home, AttentionDir, "preflight", hashWorktree(request.WorktreeRoot)+"-"+request.RequestID+".json")
}

func WritePreflight(home string, request Preflight) (string, error) {
	if request.Schema != PreflightSchema || request.RequestID == "" || strings.TrimSpace(home) == "" {
		return "", errors.New("attention: invalid preflight document")
	}
	if request.Assessment != nil {
		if err := request.AttachAssessment(*request.Assessment); err != nil {
			return "", err
		}
	}
	path := PreflightPath(home, request)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(request, "", "  ")
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".preflight-*.tmp")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return "", err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}

func ReadPreflight(path string) (Preflight, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Preflight{}, err
	}
	var request Preflight
	if err := json.Unmarshal(data, &request); err != nil {
		return Preflight{}, err
	}
	if request.Schema != PreflightSchema {
		return Preflight{}, errors.New("attention: unsupported preflight schema")
	}
	return request, nil
}

// AttachAssessment validates a Jev result against this preflight's fixed
// options. It does not imply user approval and it never executes the intent.
func (request *Preflight) AttachAssessment(assessment Assessment) error {
	if request == nil {
		return errors.New("attention: nil preflight")
	}
	artifact := request.assessmentArtifact()
	if err := AttachAssessment(&artifact, assessment); err != nil {
		return err
	}
	request.Assessment = artifact.Assessment
	return nil
}

// AssessmentArtifactForAdapter exposes the same redacted, fixed-option shape
// as an ordinary attention artifact solely for the shared Jev client. The
// preflight document remains the source of intent and persistence.
func (request Preflight) AssessmentArtifactForAdapter() Artifact {
	return request.assessmentArtifact()
}

// ValidatePreflight checks current facts and draft content, not a model's
// recommendation. Call it with a new status snapshot immediately before the
// agent asks the person or maps that person's choice to an existing CLI.
func ValidatePreflight(request Preflight, snapshot StatusSnapshot, home string, now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	deadline, err := time.Parse(time.RFC3339Nano, request.FreshUntil)
	if err != nil || !now.Before(deadline) {
		return ErrStale
	}
	derived, err := DerivePreflight(snapshot, request.Intent, home, now)
	if err != nil {
		return err
	}
	if derived.RequestID != request.RequestID || derived.WorktreeRoot != request.WorktreeRoot || derived.StateRevision != request.StateRevision {
		return ErrStale
	}
	return nil
}

// assessmentArtifact is a translation for the typed client, not a runtime
// event publication. The preflight schema retains the actual intent/source.
func (request Preflight) assessmentArtifact() Artifact {
	return Artifact{Schema: Schema, EventID: request.RequestID, StateRevision: request.StateRevision, WorktreeRoot: request.WorktreeRoot, Branch: request.Branch, GeneratedAt: request.GeneratedAt, FreshUntil: request.FreshUntil, Event: Event{Kind: EventKind("agent_action_intent"), Service: request.Target.Service, Reason: request.Intent.Action, At: request.GeneratedAt}, Evidence: request.Evidence, Options: request.Options}
}
