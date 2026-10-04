package client

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
)

// TestCheckProtocol pins contract §7's versioning rule: only the major has to
// match, because additive changes bump the minor.
func TestCheckProtocol(t *testing.T) {
	major, err := strconv.Atoi(strings.Split(rpc.ProtocolVersion, ".")[0])
	if err != nil {
		t.Fatal(err)
	}
	same := func(suffix string) string { return fmt.Sprintf("%d%s", major, suffix) }
	tests := []struct {
		daemon    string
		wantMatch bool
	}{
		{rpc.ProtocolVersion, true},
		{same(".0.0"), true},
		{same(".4.0"), true},
		{same(".0.99"), true},
		{"v" + same(".2.3"), true},
		{" " + same(".2.3") + " ", true},
		{fmt.Sprintf("%d.0.0", major+1), false},
		{fmt.Sprintf("%d.9.0", major+2), false},
	}

	for _, tt := range tests {
		err := CheckProtocol(tt.daemon)
		if tt.wantMatch && err != nil {
			t.Errorf("CheckProtocol(%q) = %v, want nil", tt.daemon, err)
			continue
		}
		if !tt.wantMatch {
			var mismatch *ProtocolMismatchError
			if !errors.As(err, &mismatch) {
				t.Errorf("CheckProtocol(%q) = %v, want a ProtocolMismatchError", tt.daemon, err)
				continue
			}
			if !strings.Contains(mismatch.Error(), "oberth daemon restart") {
				t.Errorf("mismatch message has no restart hint: %q", mismatch.Error())
			}
		}
	}
}

func TestCheckProtocolRejectsGarbage(t *testing.T) {
	for _, v := range []string{"", "   ", "one.two.three", "x"} {
		if err := CheckProtocol(v); err == nil {
			t.Errorf("CheckProtocol(%q) accepted an unparseable version", v)
		}
	}
}

func TestSubscribeOptionsInclude(t *testing.T) {
	tests := []struct {
		opts SubscribeOptions
		want string
	}{
		{SubscribeOptions{}, ""},
		{SubscribeOptions{Stats: true}, "stats"},
		{SubscribeOptions{Health: true}, "health"},
		{SubscribeOptions{Stats: true, Health: true}, "stats,health"},
	}
	for _, tt := range tests {
		got := strings.Join(tt.opts.include(), ",")
		if got != tt.want {
			t.Errorf("include() for %+v = %q, want %q", tt.opts, got, tt.want)
		}
	}
}
