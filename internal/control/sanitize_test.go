package control

import (
	"strings"
	"testing"
)

func TestSanitizeTerminalStripsEscCSIAndOSC(t *testing.T) {
	cases := map[string]string{
		"safe title":                       "safe title",
		"\x1b[2J\x1b[Howned node":          "owned node",
		"node\x1b]0;evil title\x07 name":   "node name",
		"node\x1b]8;;https://evil\x1b\\ x": "node x",
		"a\r\nb\tc":                        "a b c",
		"bidirectional \u202e spoof":       "bidirectional spoof",
		"isolate \u2066secret\u2069 text":  "isolate secret text",
		"nul\x00byte":                      "nulbyte",
		"c1 \u009b2J text":                 "c1 2J text",
	}
	for in, want := range cases {
		got := SanitizeTerminal(in)
		if got != want {
			t.Errorf("SanitizeTerminal(%q) = %q, want %q", in, got, want)
		}
		if strings.ContainsRune(got, 0x1b) {
			t.Errorf("ESC survived sanitization: %q", got)
		}
	}
}

func TestSanitizeTerminalTruncatesToContractLimit(t *testing.T) {
	in := strings.Repeat("a", 200)
	got := SanitizeTerminal(in)
	if len([]rune(got)) != 120 {
		t.Fatalf("length = %d, want 120", len([]rune(got)))
	}
}

func TestSanitizerKeepsForeignUnicode(t *testing.T) {
	if got := SanitizeTerminal("نام گره"); got != "نام گره" {
		t.Fatalf("foreign text changed: %q", got)
	}
}
