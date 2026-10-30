package groups

import (
	"fmt"
	"os"
	"strings"
)

// MaxEnvFileBytes bounds one `env_file:`. It is large enough for a normal
// development environment and small enough that a mistaken path (a database
// file, a disk image) is refused before it is read into memory.
const MaxEnvFileBytes = 4 << 20

// EnvFileValues reads a dotenv-style file. Blank lines and lines starting with
// `#` are ignored; an optional `export ` prefix is accepted; a key and value
// are separated by the first `=`. A quoted value keeps its inside verbatim
// (single quotes) or with the usual \n, \r, \t, \\ and \" escapes (double
// quotes); an unquoted value is trimmed at both ends. Values are not
// interpolated and no reference is expanded: an env file is data, and a secret
// containing `${` must survive unchanged.
//
// A malformed line is an error, not a skipped line: a service silently missing
// half its environment is harder to diagnose than a refused start. Errors name
// the file, the line and the key, never the value.
func EnvFileValues(path string) (map[string]string, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("env_file path is empty")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("reading env_file %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("env_file %s is not a regular file", path)
	}
	if info.Size() > MaxEnvFileBytes {
		return nil, fmt.Errorf("env_file %s is larger than %d bytes", path, MaxEnvFileBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading env_file %s: %w", path, err)
	}
	out := map[string]string{}
	text := strings.TrimPrefix(strings.ReplaceAll(string(data), "\r\n", "\n"), "\ufeff")
	for i, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "export"); ok && (rest == "" || rest[0] == ' ' || rest[0] == '\t') {
			line = strings.TrimSpace(rest)
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s:%d: expected KEY=VALUE", path, i+1)
		}
		key = strings.TrimSpace(key)
		if !envKey.MatchString(key) {
			return nil, fmt.Errorf("%s:%d: %q is not a variable name", path, i+1, key)
		}
		value, err = envFileValue(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %v", path, i+1, err)
		}
		out[key] = value
	}
	return out, nil
}

// envFileValue removes one matching pair of quotes and, for double quotes,
// applies the escapes dotenv files use in them. An unquoted value keeps `#`
// and `$` as written.
func envFileValue(value string) (string, error) {
	if len(value) < 2 {
		return value, nil
	}
	switch value[0] {
	case '\'':
		if !strings.HasSuffix(value, "'") {
			return "", fmt.Errorf("unterminated single quote")
		}
		return value[1 : len(value)-1], nil
	case '"':
		if !strings.HasSuffix(value, `"`) {
			return "", fmt.Errorf("unterminated double quote")
		}
		return unescapeDouble(value[1 : len(value)-1])
	}
	return value, nil
}

func unescapeDouble(value string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] != '\\' {
			b.WriteByte(value[i])
			continue
		}
		if i+1 >= len(value) {
			return "", fmt.Errorf("trailing backslash in double-quoted value")
		}
		i++
		switch value[i] {
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case '\\', '"':
			b.WriteByte(value[i])
		default:
			// Keep an unknown escape as written rather than inventing a value.
			b.WriteByte('\\')
			b.WriteByte(value[i])
		}
	}
	return b.String(), nil
}
