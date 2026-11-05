package client

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
)

// Only the major protocol version has to match: additive changes bump the
// minor, so a client accepts any daemon on its own major.
func TestCheckProtocolMajorOnly(t *testing.T) {
	major, err := strconv.Atoi(strings.Split(rpc.ProtocolVersion, ".")[0])
	if err != nil {
		t.Fatal(err)
	}
	atMinor := func(suffix string) string { return fmt.Sprintf("%d%s", major, suffix) }

	accepted := []string{
		rpc.ProtocolVersion,
		atMinor(".0.0"),
		atMinor(".4.0"),
		atMinor(".0.99"),
		"v" + atMinor(".2.3"),
		" " + atMinor(".2.3") + " ",
	}
	for _, daemon := range accepted {
		if err := CheckProtocol(daemon); err != nil {
			t.Errorf("CheckProtocol(%q) = %v, want nil", daemon, err)
		}
	}

	refused := []string{
		fmt.Sprintf("%d.0.0", major+1),
		fmt.Sprintf("%d.9.0", major+2),
	}
	for _, daemon := range refused {
		err := CheckProtocol(daemon)
		var mismatch *ProtocolMismatchError
		if !errors.As(err, &mismatch) {
			t.Errorf("CheckProtocol(%q) = %v, want a ProtocolMismatchError", daemon, err)
			continue
		}
		if !strings.Contains(mismatch.Error(), "oberth daemon restart") {
			t.Errorf("mismatch message has no restart hint: %q", mismatch.Error())
		}
	}
}

func TestCheckProtocolRejectsUnparseableVersions(t *testing.T) {
	for _, version := range []string{"", "   ", "one.two.three", "x"} {
		if err := CheckProtocol(version); err == nil {
			t.Errorf("CheckProtocol(%q) accepted a bad version", version)
		}
	}
}

func TestSubscribeInclude(t *testing.T) {
	cases := []struct {
		options SubscribeOptions
		want    string
	}{
		// Nothing requested.
		{SubscribeOptions{}, ""},
		// Each kind on its own.
		{SubscribeOptions{Stats: true}, "stats"},
		{SubscribeOptions{Health: true}, "health"},
		// Both, in the order the wire expects.
		{SubscribeOptions{Stats: true, Health: true}, "stats,health"},
	}
	for _, tc := range cases {
		if got := strings.Join(tc.options.include(), ","); got != tc.want {
			t.Errorf("include() for %+v = %q, want %q", tc.options, got, tc.want)
		}
	}
}
