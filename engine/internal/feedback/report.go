package feedback

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// ReportSchema marks the issue body so a maintainer can tell a filed report
// from a hand-written issue without reading it.
const ReportSchema = "oberth.feedback/v1"

// Kind is the fixed vocabulary a report may use. It is deliberately small:
// free text is what a note is for, and a note needs confirmation.
type Kind string

const (
	// KindSlow: a command or a start took far longer than the work it
	// describes.
	KindSlow Kind = "slow"
	// KindStalled: no progress and no output while waiting.
	KindStalled Kind = "stalled"
	// KindCoarse: the published facts were too coarse to decide what to do.
	KindCoarse Kind = "coarse"
	// KindMisleading: a published fact or hint did not match what happened.
	KindMisleading Kind = "misleading"
	// KindObservation: the listener table could not be read.
	KindObservation Kind = "observation"
	// KindOther: something else about using oberth.
	KindOther Kind = "other"
)

var kindLabels = map[Kind]string{
	KindSlow:        "a command or a start took far longer than the work it describes",
	KindStalled:     "no progress and no output while waiting",
	KindCoarse:      "the published facts were too coarse to decide what to do",
	KindMisleading:  "a published fact or hint did not match what happened",
	KindObservation: "the listener table could not be read",
	KindOther:       "something else about using oberth",
}

// kindShort is the same vocabulary for a terminal list: a row has to fit one
// line, and the issue body keeps the full sentence above.
var kindShort = map[Kind]string{
	KindSlow:        "a command or start was far slower than the work",
	KindStalled:     "no progress and no output while waiting",
	KindCoarse:      "the facts were too coarse to decide what to do",
	KindMisleading:  "a published fact or hint did not match",
	KindObservation: "the listener table could not be read",
	KindOther:       "something else about using oberth",
}

// KindOption is one row of the report-kind vocabulary: the identifier a report
// stores, the sentence the issue carries, and the short line a picker shows.
type KindOption struct {
	Kind  Kind
	Label string
	Short string
}

// KindOptions returns the vocabulary in the same stable order as KindNames,
// with the words a person reads instead of a second hand-written list in the
// command.
func KindOptions() []KindOption {
	names := KindNames()
	options := make([]KindOption, 0, len(names))
	for _, name := range names {
		kind := Kind(name)
		options = append(options, KindOption{
			Kind: kind, Label: kindLabels[kind], Short: kindShort[kind],
		})
	}
	return options
}

// Command is the oberth command the report is about.
type Command string

const (
	CommandStatus     Command = "status"
	CommandUp         Command = "up"
	CommandRestart    Command = "restart"
	CommandDown       Command = "down"
	CommandStart      Command = "start"
	CommandKill       Command = "kill"
	CommandLogs       Command = "logs"
	CommandEvents     Command = "events"
	CommandWorktrees  Command = "worktrees"
	CommandWorkplace  Command = "workplace"
	CommandInit       Command = "init"
	CommandGit        Command = "git"
	CommandAgent      Command = "agent"
	CommandAttn       Command = "attn"
	CommandDoctor     Command = "doctor"
	CommandDaemon     Command = "daemon"
	CommandServe      Command = "serve"
	CommandConfig     Command = "config"
	CommandVersion    Command = "version"
	CommandCompletion Command = "completion"
	CommandFeedback   Command = "feedback"
	CommandOther      Command = "other"
)

var commands = []Command{
	CommandStatus, CommandUp, CommandRestart, CommandDown, CommandStart, CommandKill,
	CommandLogs, CommandEvents, CommandWorktrees, CommandWorkplace, CommandInit,
	CommandGit, CommandAgent, CommandAttn, CommandDoctor, CommandDaemon, CommandServe,
	CommandConfig, CommandVersion, CommandCompletion, CommandFeedback, CommandOther,
}

// observationStates are the values status publishes in observation.state.
var observationStates = map[string]bool{
	state.ObservationObservable: true,
	state.ObservationPartial:    true,
	state.ObservationBlind:      true,
	state.ObservationBindDenied: true,
}

// KindNames lists the vocabulary for help text and tests, in a stable order.
func KindNames() []string {
	names := make([]string, 0, len(kindLabels))
	for kind := range kindLabels {
		names = append(names, string(kind))
	}
	sort.Strings(names)
	return names
}

// CommandNames lists the commands a report may name, in a stable order.
func CommandNames() []string {
	names := make([]string, len(commands))
	for i, command := range commands {
		names[i] = string(command)
	}
	return names
}

// Input is what the caller states. Everything here is validated or bucketed;
// none of it is read from the machine.
type Input struct {
	Kind        string
	Command     string
	ErrorCode   string
	Observation string
	Elapsed     string
	// Services is the declared service count, or -1 when the caller did not
	// state one.
	Services int
	Note     string
	Version  string
	Commit   string
	Platform string
	Now      time.Time
	Redactor Redactor
}

// Report is a validated, ready-to-send report.
type Report struct {
	Kind        Kind
	Command     Command
	ErrorCode   string
	Observation string
	Elapsed     string
	Services    string
	Note        string
	Version     string
	Commit      string
	Platform    string
	At          time.Time
}

// Build validates the stated facts and produces the report. A refusal here is
// the caller's to fix; nothing is sent.
func Build(in Input) (Report, error) {
	kind, err := parseKind(in.Kind)
	if err != nil {
		return Report{}, err
	}
	command, err := parseCommand(in.Command)
	if err != nil {
		return Report{}, err
	}
	errorCode := strings.TrimSpace(in.ErrorCode)
	if errorCode != "" && !knownErrorCode(errorCode) {
		return Report{}, fmt.Errorf("error code %q is not one the engine publishes", errorCode)
	}
	observation := strings.TrimSpace(in.Observation)
	if observation != "" && !observationStates[observation] {
		return Report{}, fmt.Errorf("observation %q is not observable, partial, blind or bind_denied", observation)
	}
	elapsed := ""
	if strings.TrimSpace(in.Elapsed) != "" {
		duration, err := time.ParseDuration(strings.TrimSpace(in.Elapsed))
		if err != nil || duration < 0 {
			return Report{}, fmt.Errorf("elapsed %q is not a duration such as 45s or 2m", in.Elapsed)
		}
		elapsed = elapsedBucket(duration)
	}
	services := ""
	if in.Services >= 0 {
		services = servicesBucket(in.Services)
	}
	note, err := in.Redactor.NormalizeNote(in.Note)
	if err != nil {
		return Report{}, err
	}
	version := strings.TrimSpace(in.Version)
	if version == "" {
		return Report{}, fmt.Errorf("the report needs the oberth version")
	}
	platform := strings.TrimSpace(in.Platform)
	if platform == "" {
		return Report{}, fmt.Errorf("the report needs a platform")
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	return Report{
		Kind:        kind,
		Command:     command,
		ErrorCode:   errorCode,
		Observation: observation,
		Elapsed:     elapsed,
		Services:    services,
		Note:        note,
		Version:     version,
		Commit:      shortCommit(in.Commit),
		Platform:    platform,
		At:          now.UTC(),
	}, nil
}

func parseKind(raw string) (Kind, error) {
	kind := Kind(strings.TrimSpace(raw))
	if _, ok := kindLabels[kind]; !ok {
		return "", fmt.Errorf("kind %q is not one of %s", raw, strings.Join(KindNames(), ", "))
	}
	return kind, nil
}

func parseCommand(raw string) (Command, error) {
	command := Command(strings.TrimSpace(raw))
	for _, known := range commands {
		if command == known {
			return command, nil
		}
	}
	return "", fmt.Errorf("command %q is not one of %s", raw, strings.Join(CommandNames(), ", "))
}

// cliErrorCodes are the stable codes the CLI itself puts in its JSON error
// envelope (fail / failHint in internal/cmd). The daemon never returns them,
// so they cannot live in rpc.failures: that table is the daemon's vocabulary,
// and every entry there must have a daemon producer and a line in the
// contract. A feedback report about the command that returned one still has to
// be able to name it, so the CLI's half is kept here, beside the check that
// reads both.
var cliErrorCodes = []string{
	"attn_disabled",
	"attn_invalid_config",
	"attn_no_key",
	"attn_not_configured",
	"attn_probe_failed",
	"daemon_not_running",
	"daemon_unavailable",
	"interrupted",
	"stale",
	"timeout",
}

// knownErrorCode accepts the engine's whole published vocabulary: the daemon's
// failures, read from the RPC package so the report cannot drift from that
// contract, plus the codes only the CLI's own envelope carries.
func knownErrorCode(code string) bool {
	return slices.Contains(rpc.FailureNames(), code) || slices.Contains(cliErrorCodes, code)
}

func elapsedBucket(d time.Duration) string {
	switch {
	case d < time.Second:
		return "<1s"
	case d < 5*time.Second:
		return "1-5s"
	case d < 30*time.Second:
		return "5-30s"
	case d < 2*time.Minute:
		return "30s-2m"
	default:
		return "2m+"
	}
}

func servicesBucket(n int) string {
	switch {
	case n <= 0:
		return "none"
	case n == 1:
		return "1"
	case n <= 5:
		return "2-5"
	case n <= 20:
		return "6-20"
	default:
		return "21+"
	}
}

func shortCommit(commit string) string {
	commit = strings.TrimSpace(commit)
	if commit == "" || commit == "unknown" {
		return ""
	}
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}

// Title is the issue subject. It names the vocabulary and the build so a
// maintainer can triage from the list.
func (r Report) Title() string {
	return fmt.Sprintf("[feedback] %s · %s · %s · %s", r.Kind, r.Command, r.Version, r.Platform)
}

// Body is the issue body: the structured facts, then the reporter's own words
// when there are any. It is what the preview shows and what gets filed.
func (r Report) Body() string {
	var b strings.Builder
	fmt.Fprintf(&b, "<!-- %s -->\n", ReportSchema)
	fmt.Fprintf(&b, "**Kind:** %s — %s\n", r.Kind, kindLabels[r.Kind])
	fmt.Fprintf(&b, "**Command:** `oberth %s`\n", r.Command)
	if r.Commit != "" {
		fmt.Fprintf(&b, "**Version:** %s (commit %s)\n", r.Version, r.Commit)
	} else {
		fmt.Fprintf(&b, "**Version:** %s\n", r.Version)
	}
	fmt.Fprintf(&b, "**Platform:** %s\n", r.Platform)
	if r.Elapsed != "" {
		fmt.Fprintf(&b, "**Elapsed:** %s\n", r.Elapsed)
	}
	if r.Services != "" {
		fmt.Fprintf(&b, "**Services:** %s\n", r.Services)
	}
	if r.ErrorCode != "" {
		fmt.Fprintf(&b, "**Error code:** %s\n", r.ErrorCode)
	}
	if r.Observation != "" {
		fmt.Fprintf(&b, "**Observation:** %s\n", r.Observation)
	}
	fmt.Fprintf(&b, "**Reported:** %s\n", r.At.Format("2006-01-02"))
	if r.Note != "" {
		// An indented block, not inline text: the note is the reporter's own
		// words, and Markdown is an instruction channel. Indenting keeps a
		// `@name` from notifying anyone and a stray fence from reshaping the
		// issue.
		fmt.Fprintf(&b, "\n**Note (redacted before sending):**\n\n")
		for _, line := range strings.Split(r.Note, "\n") {
			fmt.Fprintf(&b, "    %s\n", line)
		}
	}
	b.WriteString("\n---\n")
	b.WriteString("Filed by `oberth feedback report`. It carries the version, platform, command name,\n")
	b.WriteString("stable error code, observation state and timing bucket — no paths, logs, commands,\n")
	b.WriteString("manifest content or environment variables.\n")
	return b.String()
}

// Fingerprint identifies the same complaint from the same build, so a repeat
// within the dedupe window does not file a second issue. Elapsed is left out
// because it varies between sightings of one problem. The redacted note is
// part of the identity: two reports whose structured fields match can still
// describe different defects, and the note is the only place that difference
// is stated. Dropping it silently suppressed the second one and answered with
// the first one's issue.
func (r Report) Fingerprint() string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		string(r.Kind), string(r.Command), r.ErrorCode, r.Observation, r.Version, r.Note,
	}, "\x00")))
	return hex.EncodeToString(sum[:])[:12]
}

// Preview is what a person reads before a free-text report goes out.
func (r Report) Preview() string {
	return r.Title() + "\n\n" + r.Body()
}
