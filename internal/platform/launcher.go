package platform

import (
	"context"
	"strings"

	"routelatch/internal/contract"
)

// Proxy environment variable names. Both spellings are set because Go's
// net/http reads the upper and lower case forms while curl and libproxy prefer
// the lower case form for HTTP_PROXY. Setting only one spelling would let an
// inherited value win for some programs.
const (
	EnvHTTPProxy  = "HTTP_PROXY"
	EnvHTTPSProxy = "HTTPS_PROXY"
	EnvALLProxy   = "ALL_PROXY"
	EnvNoProxy    = "NO_PROXY"
)

var proxyEnvironmentKeys = map[string]struct{}{
	EnvHTTPProxy: {}, EnvHTTPSProxy: {}, EnvALLProxy: {}, EnvNoProxy: {},
	"http_proxy": {}, "https_proxy": {}, "all_proxy": {}, "no_proxy": {},
}

// LaunchNotice is shown by the CLI and, where useful, written to stderr. It
// keeps the launcher honest: environment variables are not interception, and
// RouteLatch cannot clean up a program it did not start.
const LaunchNotice = "RouteLatch changes only this child process's proxy environment. " +
	"Programs that ignore HTTP_PROXY/HTTPS_PROXY/ALL_PROXY/NO_PROXY, and programs started outside " +
	"this launcher, are not routed by RouteLatch."

// Health is a running service's own report about which listeners it bound. The
// launcher compares it with the configured addresses so a stale configuration
// or a foreign process listening on the same port cannot be mistaken for
// RouteLatch (identity check, not just a TCP connect).
type Health struct {
	HTTPListen  string
	SOCKSListen string
}

// HealthChecker verifies that the service is up and identifies its listeners.
type HealthChecker interface {
	Check(ctx context.Context) (Health, error)
}

// Launcher prepares one child process. It never starts a command itself: the
// caller receives an environment only after the health and identity checks.
type Launcher struct {
	Health HealthChecker
}

// Launch is the result of a successful preparation. An application must not be
// started when Prepare returns an error.
type Launch struct {
	Environment []string
	Notice      string
}

// Prepare verifies the service and constructs the child environment. On any
// failure it returns an empty Launch, so there is no path that runs the
// application without the proxy or with an inherited direct setting.
func (l *Launcher) Prepare(ctx context.Context, expected Targets, inherited []string) (Launch, error) {
	if err := expected.Validate(); err != nil {
		return Launch{}, err
	}
	if l == nil {
		return Launch{}, localFailure(contract.Internal, "platform.launch",
			"launcher has no health checker; refusing to start an application")
	}
	if err := verifyServiceHealth(ctx, l.Health, expected, "platform.launch"); err != nil {
		return Launch{}, err
	}

	env, err := BuildProxyEnvironment(inherited, expected)
	if err != nil {
		return Launch{}, err
	}
	return Launch{Environment: env, Notice: LaunchNotice}, nil
}

// verifyServiceHealth is shared by the launcher and the desktop adapter. A nil
// checker is a configuration error, not a reason to assume health.
func verifyServiceHealth(ctx context.Context, checker HealthChecker, expected Targets, op string) error {
	if checker == nil {
		return localFailure(contract.Internal, op,
			"no service health checker is configured; refusing to treat the service as healthy")
	}
	health, err := checker.Check(ctx)
	if err != nil {
		return localFailure(contract.Stopped, op,
			"RouteLatch is not healthy; refusing to use the configured listeners (there is no direct fallback)")
	}
	if health.HTTPListen != expected.HTTPListen || health.SOCKSListen != expected.SOCKSListen {
		return localFailure(contract.Stopped, op,
			"the running service does not own the configured listeners; refusing to use them (there is no direct fallback)")
	}
	return nil
}

// BuildProxyEnvironment replaces every inherited proxy variable, preserves all
// unrelated entries byte-for-byte, and sets both spellings of the four proxy
// variables. NO_PROXY becomes the fixed local/LAN list; an inherited entry for
// an approved destination is dropped rather than passed through.
func BuildProxyEnvironment(inherited []string, targets Targets) ([]string, error) {
	httpListener, err := targets.http()
	if err != nil {
		return nil, err
	}
	if _, err := targets.socks(); err != nil {
		return nil, err
	}

	proxyURL := "http://" + httpListener.hostPort()
	// ALL_PROXY uses the HTTP listener because its CONNECT path handles any
	// TCP destination. It is not a SOCKS URL and it does not name a fallback.
	env := make([]string, 0, len(inherited)+8)
	for _, entry := range inherited {
		key := entry
		if i := strings.IndexByte(entry, '='); i >= 0 {
			key = entry[:i]
		}
		if _, replace := proxyEnvironmentKeys[key]; replace {
			continue
		}
		env = append(env, entry)
	}
	env = append(env,
		EnvHTTPProxy+"="+proxyURL,
		EnvHTTPSProxy+"="+proxyURL,
		EnvALLProxy+"="+proxyURL,
		EnvNoProxy+"="+NoProxy,
		"http_proxy="+proxyURL,
		"https_proxy="+proxyURL,
		"all_proxy="+proxyURL,
		"no_proxy="+NoProxy,
	)
	return env, nil
}
