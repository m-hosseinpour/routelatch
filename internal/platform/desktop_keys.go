package platform

import (
	"strings"

	"routelatch/internal/contract"
)

// Kind is one supported desktop environment (docs/contracts.md §4.8).
type Kind string

const (
	KindGNOME Kind = "gnome"
	KindKDE   Kind = "kde"
)

const (
	kdeConfigFile = "kioslaverc"
	kdeGroup      = "Proxy Settings"
	// kreadconfig prints --default when a key is absent. KConfig may hold an
	// empty string, but RouteLatch never writes an empty value, so an empty
	// previous value means "delete this key" on restore.
	kdeAbsentValue = "__routelatch_absent_key_2f5d3c1a__"
)

// EnvLookup is the environment view used by desktop detection. It is injected
// so detection is a pure function in tests.
type EnvLookup func(key string) (string, bool)

// DetectDesktop maps the session environment to a supported desktop. The
// answer is advisory: Apply still requires an explicit Kind and never runs
// automatically. An unknown desktop gets the documented unsupported failure,
// not a guess and not a claim of system-wide proxying.
func DetectDesktop(lookup EnvLookup) (Kind, error) {
	if lookup == nil {
		return "", localFailure(contract.Internal, "platform.desktop.detect", "desktop detection has no environment lookup")
	}
	if current, ok := lookup("XDG_CURRENT_DESKTOP"); ok {
		for _, token := range strings.Split(current, ":") {
			switch strings.ToLower(strings.TrimSpace(token)) {
			case "gnome":
				return KindGNOME, nil
			case "kde", "plasma":
				return KindKDE, nil
			}
		}
	}
	if session, ok := lookup("DESKTOP_SESSION"); ok {
		s := strings.ToLower(session)
		if strings.Contains(s, "gnome") {
			return KindGNOME, nil
		}
		if strings.Contains(s, "plasma") || strings.Contains(s, "kde") {
			return KindKDE, nil
		}
	}
	if v, ok := lookup("KDE_FULL_SESSION"); ok && strings.EqualFold(strings.TrimSpace(v), "true") {
		return KindKDE, nil
	}
	if v, ok := lookup("GNOME_DESKTOP_SESSION_ID"); ok && strings.TrimSpace(v) != "" {
		return KindGNOME, nil
	}
	return "", localFailure(contract.Unsupported, "platform.desktop.detect",
		"this desktop is not supported for automatic proxy settings; only GNOME (gsettings) and KDE Plasma (kwriteconfig) are supported")
}

func validKind(k Kind) bool { return k == KindGNOME || k == KindKDE }

// keyLayout identifies one desktop setting without its value. Apply order is
// the slice order (mode last); Restore walks it in reverse (mode first).
type keyLayout struct {
	name   string
	schema string // GNOME only
	key    string // GNOME key or KDE key
}

func layoutsFor(kind Kind) []keyLayout {
	switch kind {
	case KindGNOME:
		return []keyLayout{
			{name: "org.gnome.system.proxy.http host", schema: "org.gnome.system.proxy.http", key: "host"},
			{name: "org.gnome.system.proxy.http port", schema: "org.gnome.system.proxy.http", key: "port"},
			{name: "org.gnome.system.proxy.https host", schema: "org.gnome.system.proxy.https", key: "host"},
			{name: "org.gnome.system.proxy.https port", schema: "org.gnome.system.proxy.https", key: "port"},
			{name: "org.gnome.system.proxy.socks host", schema: "org.gnome.system.proxy.socks", key: "host"},
			{name: "org.gnome.system.proxy.socks port", schema: "org.gnome.system.proxy.socks", key: "port"},
			{name: "org.gnome.system.proxy ignore-hosts", schema: "org.gnome.system.proxy", key: "ignore-hosts"},
			{name: "org.gnome.system.proxy mode", schema: "org.gnome.system.proxy", key: "mode"},
		}
	case KindKDE:
		// ProxyType manual is 1 (KDE's ProxyType enum: NoProxy, ManualProxy,
		// PACProxy, WPADProxy, EnvVarProxy). ReversedException must be false,
		// otherwise NoProxyFor is used as an allow-list and every other
		// destination would bypass the proxy.
		return []keyLayout{
			{name: "kioslaverc:Proxy Settings/ReversedException", key: "ReversedException"},
			{name: "kioslaverc:Proxy Settings/httpProxy", key: "httpProxy"},
			{name: "kioslaverc:Proxy Settings/httpsProxy", key: "httpsProxy"},
			{name: "kioslaverc:Proxy Settings/socksProxy", key: "socksProxy"},
			{name: "kioslaverc:Proxy Settings/NoProxyFor", key: "NoProxyFor"},
			{name: "kioslaverc:Proxy Settings/ProxyType", key: "ProxyType"},
		}
	default:
		return nil
	}
}

func gvariantString(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

func gvariantStringList(list string) string {
	parts := strings.Split(list, ",")
	for i, part := range parts {
		parts[i] = gvariantString(strings.TrimSpace(part))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func kdeProxyURL(scheme string, l listener) string {
	host := l.host
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	// KDE's proxy KCM stores "url port" (a space before the port), which is
	// also what libproxy reads from kioslaverc.
	return scheme + "://" + host + " " + l.port
}

// valuesFor builds the exact strings RouteLatch writes. None of these values is
// derived from an inherited proxy variable.
func valuesFor(kind Kind, targets Targets) (map[string]string, error) {
	httpListener, err := targets.http()
	if err != nil {
		return nil, err
	}
	socksListener, err := targets.socks()
	if err != nil {
		return nil, err
	}
	switch kind {
	case KindGNOME:
		return map[string]string{
			"org.gnome.system.proxy.http host":    gvariantString(httpListener.host),
			"org.gnome.system.proxy.http port":    httpListener.port,
			"org.gnome.system.proxy.https host":   gvariantString(httpListener.host),
			"org.gnome.system.proxy.https port":   httpListener.port,
			"org.gnome.system.proxy.socks host":   gvariantString(socksListener.host),
			"org.gnome.system.proxy.socks port":   socksListener.port,
			"org.gnome.system.proxy ignore-hosts": gvariantStringList(NoProxy),
			"org.gnome.system.proxy mode":         gvariantString("manual"),
		}, nil
	case KindKDE:
		return map[string]string{
			"kioslaverc:Proxy Settings/ReversedException": "false",
			"kioslaverc:Proxy Settings/httpProxy":         kdeProxyURL("http", httpListener),
			"kioslaverc:Proxy Settings/httpsProxy":        kdeProxyURL("http", httpListener),
			"kioslaverc:Proxy Settings/socksProxy":        kdeProxyURL("socks", socksListener),
			"kioslaverc:Proxy Settings/NoProxyFor":        NoProxy,
			"kioslaverc:Proxy Settings/ProxyType":         "1",
		}, nil
	default:
		return nil, localFailure(contract.Unsupported, "platform.desktop.apply",
			"only GNOME and KDE Plasma desktop proxy settings are supported")
	}
}
