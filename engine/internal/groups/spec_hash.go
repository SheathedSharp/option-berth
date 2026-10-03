package groups

import (
	"crypto/sha256"
	"encoding/base32"
	"strconv"
)

// ServiceSpecHash identifies the executable part of a manifest service. It
// deliberately excludes env values and presentation metadata: runtime state
// can prove which command/cwd/port/health/depends_on declaration was used
// without copying secrets into the registry or state stream.
//
// The encoding is a hand-rolled stable serialization, not json.Marshal: the
// field order and framing below are the format, and changing them changes
// every hash. A service name may hold any character a YAML scalar can, so
// every field is length-prefixed rather than delimited. The digest renders as
// lowercase base32 without padding — 52 characters, no case folding against
// hex, one output string per service.
func ServiceSpecHash(s Service) string {
	buf := make([]byte, 0, specHashBufCap(&s))
	buf = appendHashField(buf, s.Name)
	buf = appendHashField(buf, s.Prepare)
	buf = appendHashField(buf, s.Cmd)
	buf = appendHashField(buf, s.Cwd)
	buf = strconv.AppendInt(buf, int64(s.Port), 10)
	buf = append(buf, ';')
	buf = strconv.AppendBool(buf, s.PortAuto)
	buf = append(buf, ';')
	buf = appendHashField(buf, s.Health)
	buf = strconv.AppendInt(buf, int64(len(s.DependsOn)), 10)
	buf = append(buf, ';')
	for _, dep := range s.DependsOn {
		buf = appendHashField(buf, dep)
	}
	sum := sha256.Sum256(buf)
	return base32Hex.EncodeToString(sum[:])
}

// SpecHashLen is the length of one ServiceSpecHash string: 256 bits in
// lowercase unpadded base32. Consumers treat a runtime hash of any other
// length as pre-upgrade evidence rather than drift (see runtime.go).
const SpecHashLen = 52

// base32Hex is lowercase base32 without padding. Crockford is not used: the
// alphabet only needs to be stable and unambiguous, and the standard
// lowercase hex-alphabet variant avoids both uppercase and padding.
var base32Hex = base32.NewEncoding("0123456789abcdefghijklmnopqrstuv").WithPadding(base32.NoPadding)

// appendHashField appends one length-prefixed field. The prefix is what keeps
// concatenations unambiguous: ("ab", "c") and ("a", "bc") hash differently.
func appendHashField(buf []byte, s string) []byte {
	buf = strconv.AppendInt(buf, int64(len(s)), 10)
	buf = append(buf, ':')
	return append(buf, s...)
}

// specHashBufCap sizes the one scratch buffer one hash needs. The count of
// depends_on entries is part of the framing, so a cap below the serialized
// length only costs the slice growing once, never a wrong hash.
func specHashBufCap(s *Service) int {
	n := 64
	n += len(s.Name) + len(s.Prepare) + len(s.Cmd) + len(s.Cwd) + len(s.Health)
	for _, dep := range s.DependsOn {
		n += len(dep) + 4
	}
	return n
}
