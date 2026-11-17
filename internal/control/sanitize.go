package control

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"routelatch/internal/contract"
)

// SanitizeTerminal makes one untrusted runtime string safe to print in a
// terminal or a menu frame (docs/contracts.md §7.4). It removes ESC sequences
// (CSI, OSC, DCS and generic two-byte forms), C0/C1 control characters, and
// Unicode bidirectional/isolate controls; it turns CR/LF/TAB into spaces and
// truncates to contract.MaxNodeNameRunes runes.
//
// The pinned huh version renders strings unchanged, so this function is the
// boundary, not a formatting nicety.
func SanitizeTerminal(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	inEscape := false
	escKind := byte(0)
	for i := 0; i < len(s); {
		c := s[i]
		if inEscape {
			switch escKind {
			case '[': // CSI: parameters until a final byte 0x40-0x7e
				if c >= 0x40 && c <= 0x7e {
					inEscape = false
				}
				i++
				continue
			case ']', 'S': // OSC/DCS/APC/PM: until BEL or ST
				if c == 0x07 {
					inEscape = false
					i++
					continue
				}
				if c == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
					inEscape = false
					i += 2
					continue
				}
				i++
				continue
			default: // two-byte sequence: consume one byte
				inEscape = false
				i++
				continue
			}
		}
		if c == 0x9b { // 8-bit CSI
			inEscape = true
			escKind = '['
			i++
			continue
		}
		if c == 0x1b {
			if i+1 < len(s) {
				next := s[i+1]
				if next == '[' || next == ']' || next == 'P' || next == '^' || next == '_' {
					inEscape = true
					if next == '[' {
						escKind = '['
					} else if next == ']' {
						escKind = ']'
					} else {
						escKind = 'S'
					}
					i += 2
					continue
				}
				inEscape = true
				escKind = 0
				i++
				continue
			}
			i++
			continue
		}
		if c < 0x20 || c == 0x7f {
			switch c {
			case '\n', '\r', '\t':
				b.WriteByte(' ')
			}
			i++
			continue
		}
		if c >= 0x80 && c <= 0x9f {
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == unicode.ReplacementChar && size == 1 {
			i++
			continue
		}
		switch {
		case r >= 0x80 && r <= 0x9f,
			r >= 0x202a && r <= 0x202e,
			r >= 0x2066 && r <= 0x2069,
			r == 0x200e || r == 0x200f || r == 0x061c,
			r == 0x2028 || r == 0x2029:
			i += size
			continue
		}
		b.WriteString(s[i : i+size])
		i += size
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	r := []rune(out)
	if len(r) > contract.MaxNodeNameRunes {
		out = string(r[:contract.MaxNodeNameRunes])
	}
	return out
}
