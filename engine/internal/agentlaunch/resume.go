package agentlaunch

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ResumeReference contains only the selected provider's identity metadata, never
// its messages. Approval and tool execution remain in the provider's native UI.
type ResumeReference struct {
	SessionID     string `json:"session_id"`
	SourceVersion string `json:"source_version"`
	Worktree      string `json:"worktree"`
	Source        string `json:"source"`
	ApprovalOwner string `json:"approval_owner"`
}

var sessionUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var openCodeID = regexp.MustCompile(`^ses_[A-Za-z0-9]{8,128}$`)
var cliVersion = regexp.MustCompile(`^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$`)

// inspectResume reads only an explicitly selected regular file. It does not scan
// another application's history, create a conversation, or guess the last ID.
func inspectResume(provider, path, worktree string) (*ResumeReference, error) {
	refuse := func() (*ResumeReference, error) {
		return nil, errors.New("session metadata is missing, unsupported or inconsistent; select an original provider session file for this worktree")
	}
	if provider == "deepseek" {
		return nil, errors.New("DeepSeek explicit native session-file continuation is not supported; resume within its native interface")
	}
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return refuse()
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return refuse()
	}
	file, err := openSessionFile(canonical)
	if err != nil {
		return refuse()
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return refuse()
	}
	var id, root, version string
	switch provider {
	case "opencode":
		// `opencode export ID` starts with an `info` object. A bounded streaming
		// decoder extracts that object without loading its messages into memory.
		decoder := json.NewDecoder(io.LimitReader(file, 256*1024))
		token, err := decoder.Token()
		if err != nil || token != json.Delim('{') {
			return refuse()
		}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return refuse()
			}
			if key == "info" {
				var header struct {
					ID        string `json:"id"`
					Directory string `json:"directory"`
					Version   string `json:"version"`
				}
				if decoder.Decode(&header) != nil {
					return refuse()
				}
				id, root, version = header.ID, header.Directory, header.Version
				break
			}
			var skip json.RawMessage
			if decoder.Decode(&skip) != nil {
				return refuse()
			}
		}
		if !openCodeID.MatchString(id) || !cliVersion.MatchString(version) {
			return refuse()
		}
	case "codex", "claude", "pi":
		scan := bufio.NewScanner(io.LimitReader(file, 256*1024))
		scan.Buffer(make([]byte, 4096), 128*1024)
		for line := 0; line < 64 && scan.Scan(); line++ {
			var entry struct {
				Type      string          `json:"type"`
				ID        string          `json:"id"`
				CWD       string          `json:"cwd"`
				Version   json.RawMessage `json:"version"`
				SessionID string          `json:"sessionId"`
				Payload   struct {
					ID      string `json:"id"`
					CWD     string `json:"cwd"`
					Version string `json:"cli_version"`
				} `json:"payload"`
			}
			if json.Unmarshal(scan.Bytes(), &entry) != nil {
				return refuse()
			}
			switch provider {
			case "codex":
				if line != 0 || entry.Type != "session_meta" {
					return refuse()
				}
				id, root, version = entry.Payload.ID, entry.Payload.CWD, entry.Payload.Version
			case "pi":
				if line != 0 || entry.Type != "session" {
					return refuse()
				}
				// Pi JSONL format version is distinct from its CLI semver.
				if string(entry.Version) != "3" {
					return refuse()
				}
				id, root, version = entry.ID, entry.CWD, "session-v3"
			case "claude":
				if entry.SessionID == "" || entry.CWD == "" {
					continue
				}
				id, root = entry.SessionID, entry.CWD
				if json.Unmarshal(entry.Version, &version) != nil {
					return refuse()
				}
			}
			break
		}
		if !sessionUUID.MatchString(id) || (provider != "pi" && !cliVersion.MatchString(version)) {
			return refuse()
		}
	default:
		return refuse()
	}
	if !filepath.IsAbs(root) || strings.ContainsRune(root, 0) {
		return refuse()
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return refuse()
	}
	selectedInfo, err := os.Stat(worktree)
	if err != nil {
		return refuse()
	}
	recordedInfo, err := os.Stat(root)
	if err != nil || !recordedInfo.IsDir() || !os.SameFile(selectedInfo, recordedInfo) {
		return nil, errors.New("session belongs to a different or unavailable worktree; cross-worktree continuation refused")
	}
	return &ResumeReference{SessionID: id, SourceVersion: version, Worktree: root, Source: canonical, ApprovalOwner: "provider_native"}, nil
}

func resumeArguments(provider string, reference *ResumeReference, prompt string) []string {
	var args []string
	switch provider {
	case "opencode":
		args = []string{"--session=" + reference.SessionID}
	case "codex":
		args = []string{"resume", reference.SessionID}
	case "claude":
		args = []string{"--resume", reference.SessionID}
	case "pi":
		args = []string{"--session", reference.Source}
	}
	if prompt != "" {
		if provider == "opencode" {
			args = append(args, "--prompt="+prompt)
		} else {
			args = append(args, "--", prompt)
		}
	}
	return args
}
