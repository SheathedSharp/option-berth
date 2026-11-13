package display

import "testing"

// StripANSI is the rendering half of the no-color rule: the service's colour
// codes leave the printed line, and nothing else does. Cursor movement and
// screen control are not colour and stay; stripping them would change more of
// the service's output than the rule promises.
func TestStripANSIRemovesOnlyColorSequences(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"colour around a word", "\x1b[31mERROR\x1b[0m could not bind", "ERROR could not bind"},
		{"several attributes", "\x1b[1;32mok\x1b[0m", "ok"},
		{"256-colour slot", "\x1b[38;5;196mred\x1b[0m", "red"},
		{"truecolor", "\x1b[38;2;168;84;62mberth\x1b[0m", "berth"},
		{"plain line untouched", "plain line", "plain line"},
		{"newline kept", "\x1b[33mwarn\x1b[0m\n", "warn\n"},
		{"cursor movement is not colour", "keep \x1b[2J screen control", "keep \x1b[2J screen control"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StripANSI(tc.in); got != tc.want {
				t.Fatalf("StripANSI(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
