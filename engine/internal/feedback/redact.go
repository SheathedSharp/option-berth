package feedback

import (
	"errors"
	"os"
	"regexp"
	"runtime"
	"strings"
	"unicode/utf8"
)

// MaxNoteBytes bounds the reporter's own words. It is small on purpose: the
// issue URL carries the body, and a note long enough to hold a log is a note
// that should have been a paragraph.
const MaxNoteBytes = 800

// ErrSecret is returned instead of redacting. A credential that made it into a
// note is a mistake to remove, not a substring to blank out: redaction that
// guesses wrong at this point is worse than refusing.
var ErrSecret = errors.New("the note contains something that looks like a credential")

// Redactor blanks the shapes that identify a person or a project. Every rule
// is deterministic and testable; nothing here inspects the network or the
// machine beyond the home directory and login name it was built with.
type Redactor struct {
	Home string
	User string
}

// NewRedactor reads the two facts redaction needs from this process.
func NewRedactor() Redactor {
	home, _ := os.UserHomeDir()
	user := os.Getenv("USER")
	if user == "" {
		user = os.Getenv("USERNAME")
	}
	return Redactor{Home: strings.TrimSpace(home), User: strings.TrimSpace(user)}
}

var (
	secretPattern = regexp.MustCompile(`(?i)(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|sk-[A-Za-z0-9_-]{16,}|AKIA[0-9A-Z]{16}|xox[baprs]-[A-Za-z0-9-]{10,}|AIza[0-9A-Za-z_-]{30,}|bearer\s+[A-Za-z0-9._-]{20,}|-----BEGIN [A-Z ]*PRIVATE KEY-----)`)
	jwtPattern    = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{6,}`)
	urlPattern    = regexp.MustCompile(`https?://[^\s<>"']+`)
	emailPattern  = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	ipv4Pattern   = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}\b`)
	windowsPath   = regexp.MustCompile(`(?i)\b[A-Z]:\\[^\s"'` + "`" + `]+`)
	uncPath       = regexp.MustCompile(`\\\\[^\s"'` + "`" + `]+`)
	tildePath     = regexp.MustCompile(`(^|[\s"'` + "`" + `(\[])(~/[^\s"'` + "`" + `()\[\]]+)`)
	posixPath     = regexp.MustCompile(`(^|[\s"'` + "`" + `(\[])(/[^\s"'` + "`" + `()\[\]]+)`)
)

const pathPrefix = `${1}<path>`

// Redact returns text with identifying shapes replaced by a fixed marker. It
// refuses (ErrSecret) rather than guessing when the text looks like it carries
// a credential; callers must treat that as "nothing was sent".
func (r Redactor) Redact(text string) (string, error) {
	if !utf8.ValidString(text) {
		return "", errors.New("the note is not valid UTF-8")
	}
	if secretPattern.MatchString(text) || jwtPattern.MatchString(text) {
		return "", ErrSecret
	}
	text = urlPattern.ReplaceAllString(text, "<url>")
	text = emailPattern.ReplaceAllString(text, "<email>")
	text = ipv4Pattern.ReplaceAllString(text, "<ip>")
	text = windowsPath.ReplaceAllString(text, "<path>")
	text = uncPath.ReplaceAllString(text, "<path>")
	if r.Home != "" && len(r.Home) > 1 {
		text = strings.ReplaceAll(text, r.Home+"/", "~/")
		text = strings.ReplaceAll(text, r.Home+`\`, `~\`)
		exact := regexp.MustCompile(regexp.QuoteMeta(r.Home) + `\b`)
		text = exact.ReplaceAllString(text, "~")
	}
	text = tildePath.ReplaceAllString(text, pathPrefix)
	text = posixPath.ReplaceAllString(text, pathPrefix)
	if r.User != "" && len(r.User) > 2 {
		user := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(r.User) + `\b`)
		text = user.ReplaceAllString(text, "<user>")
	}
	return strings.TrimSpace(text), nil
}

// NormalizeNote redacts the reporter's words and enforces the size bound.
func (r Redactor) NormalizeNote(note string) (string, error) {
	note = strings.TrimSpace(note)
	if note == "" {
		return "", nil
	}
	if len(note) > MaxNoteBytes {
		return "", errors.New("the note is longer than 800 bytes; say it in fewer words")
	}
	return r.Redact(note)
}

// Platform is the os/arch pair a report carries. It is a build fact, not a
// machine fingerprint.
func Platform() string { return runtime.GOOS + "/" + runtime.GOARCH }
