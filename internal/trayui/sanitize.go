package trayui

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"unicode/utf8"

	"routelatch/internal/contract"
	"routelatch/internal/control"
)

const (
	// LabelLimit is the documented item-label bound (80 runes). HeaderLimit is
	// the header/notification bound (120 runes); both end a truncated string
	// with an ellipsis (docs/contracts.md §7.3).
	LabelLimit  = 80
	HeaderLimit = 120
)

// SanitizeLabel makes one untrusted dynamic string safe for a menu item. It
// reuses control.SanitizeTerminal, which strips ESC/OSC sequences, C0/C1
// controls and bidi/isolate controls, collapses whitespace and caps the result
// at contract.MaxNodeNameRunes, then applies the smaller 80-rune item bound.
func SanitizeLabel(s string) string {
	return sanitizeLimit(s, LabelLimit)
}

// SanitizeHeader is the same rule with the 120-rune header/notification bound.
func SanitizeHeader(s string) string {
	return sanitizeLimit(s, HeaderLimit)
}

func sanitizeLimit(s string, limit int) string {
	if s == "" {
		return ""
	}
	over := utf8.RuneCountInString(s) > limit
	clean := control.SanitizeTerminal(s)
	r := []rune(clean)
	if len(r) > limit || (over && len(r) == limit) {
		if limit <= 1 {
			return string(r[:limit])
		}
		return string(r[:limit-1]) + "…"
	}
	return string(r)
}

// stableKeyPart turns an id received from the management socket into a safe
// piece of a stable menu key. Contract ids are normally an ASCII id such as
// "r-1a2b" or "x-<hex>", but these strings are still untrusted input: an id
// with a control character, slash or dot must not be able to merge or escape
// keys. Unsafe values become a deterministic hash, so the same id keeps the
// same key across rebuilds.
func stableKeyPart(s string) string {
	if s == "" {
		return "none"
	}
	safe := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_' {
			continue
		}
		safe = false
		break
	}
	if safe {
		return s
	}
	sum := sha256.Sum256([]byte(s))
	return "h" + hex.EncodeToString(sum[:6])
}

// shortID is the safe, bounded id shown in a confirmation label. It never
// includes the full operand, so a confirmation label cannot leak a destination.
func shortID(s string) string {
	key := stableKeyPart(s)
	if len(key) > 10 {
		return key[:10]
	}
	return key
}

// errorKindLabel is the only error text used in a last-action line,
// notification or menu. The raw caller/server message is never rendered: it is
// the one field that could carry a destination or URL (D21).
func errorKindLabel(err error) string {
	if err == nil {
		return ""
	}
	var envErr *EnvelopeError
	if asEnvelopeError(err, &envErr) {
		if envErr.Kind == "" {
			return "internal"
		}
		return sanitizeLimit(string(envErr.Kind), LabelLimit)
	}
	var failure *contract.Failure
	if errors.As(err, &failure) && failure.Kind != "" {
		return sanitizeLimit(string(failure.Kind), LabelLimit)
	}
	if isCanceled(err) {
		return "interrupted"
	}
	if isTimeout(err) {
		return "timeout"
	}
	switch {
	case errors.Is(err, ErrBusy):
		return "busy"
	case errors.Is(err, ErrBlocked):
		return "blocked"
	case errors.Is(err, ErrNoPending):
		return "no pending confirmation"
	case errors.Is(err, ErrNoLifecycle):
		return "lifecycle unavailable"
	}
	if isUnavailable(err) {
		return "engine unavailable"
	}
	return "internal"
}
