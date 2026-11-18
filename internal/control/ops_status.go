package control

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"routelatch/internal/contract"
	"routelatch/internal/platform"
	"routelatch/internal/pool"
	"routelatch/internal/probes"
)

type listenersData struct {
	HTTP  string `json:"http"`
	SOCKS string `json:"socks"`
}

type poolStatusData struct {
	Total   int `json:"total"`
	Usable  int `json:"usable"`
	Healthy int `json:"healthy"`
	Cooling int `json:"cooling"`
}

type proofStatusData struct {
	Confirmed int `json:"confirmed"`
	Accepted  int `json:"accepted"`
}

type connectionsData struct {
	Open    int             `json:"open"`
	Dialing int             `json:"dialing"`
	Proofs  proofStatusData `json:"proofs"`
}

type diagnosticsData struct {
	Enabled bool `json:"enabled"`
	Queued  int  `json:"queued"`
}

// StatusData is the documented `status` payload.
type StatusData struct {
	Mode          contract.Mode          `json:"mode"`
	PolicyVersion contract.PolicyVersion `json:"policy_version"`
	// SelectedExit is the manual pin, not the automatic choice: empty means
	// automatic ranking. It is reported because a pin silently disables the
	// D16 ranking and is never replaced on failure (docs/contracts.md §2.5).
	SelectedExit contract.ExitID `json:"selected_exit"`
	Listeners    listenersData   `json:"listeners"`
	Pool         poolStatusData  `json:"pool"`
	Connections  connectionsData `json:"connections"`
	Diagnostics  diagnosticsData `json:"diagnostics"`
	Warnings     []string        `json:"warnings"`
}

func (s *Service) status(ctx context.Context) (StatusData, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	snap := s.Snapshot()
	now := s.now().UTC()
	p := pool.Build(snap.State, now)
	counts := p.Counts()
	out := StatusData{
		Mode: snap.State.Mode, PolicyVersion: snap.State.PolicyVersion,
		SelectedExit: snap.State.SelectedExit,
		Listeners:    listenersData{HTTP: s.httpAddr, SOCKS: s.socksAddr},
		Pool:         poolStatusData{Total: counts.Total, Usable: counts.Usable, Healthy: counts.Healthy, Cooling: counts.Cooling},
		Diagnostics:  diagnosticsData{Enabled: snap.Config.Diagnostics.Enabled},
	}
	if s.diag != nil {
		st := s.diag.Stats()
		out.Diagnostics.Queued = st.Queued
	} else {
		// Proposals are memory only (D21); report pending in-process entries
		// instead of reading a persisted list that no longer exists.
		out.Diagnostics.Queued = len(s.props.Pending())
	}
	for _, ci := range s.reg.Snapshot() {
		switch ci.State {
		case contract.ConnDialing:
			out.Connections.Dialing++
		case contract.ConnOpen:
			out.Connections.Open++
		}
		switch ci.Proof {
		case contract.ProofConfirmed:
			out.Connections.Proofs.Confirmed++
		case contract.ProofAccepted:
			out.Connections.Proofs.Accepted++
		}
	}
	warmTransports := 0
	if s.engine != nil {
		warmTransports = len(s.engine.WarmTransports())
	}
	if !s.listenersOpen {
		out.Listeners = listenersData{HTTP: s.httpListen, SOCKS: s.socksListen}
	}
	out.Warnings = statusWarnings(snap, now, s.listenersOpen, s.stopped, warmTransports)
	return out, nil
}

func statusWarnings(snap contract.Snapshot, now time.Time, listenersOpen, stopped bool, engineTransports int) []string {
	var warnings []string
	p := pool.Build(snap.State, now)
	counts := p.Counts()
	insecure := 0
	for _, rec := range p.Records() {
		// p.Records carries the derived status, so this warning and the
		// unsupported count below agree with `pool list` and `doctor`.
		if rec.Status == contract.RecordUnsupported &&
			(rec.StatusReason == contract.ReasonInsecureTLSRemoved || rec.InsecureTLS) {
			insecure++
		}
	}
	if counts.Unsupported > 0 {
		warnings = append(warnings, fmt.Sprintf("%d record(s) are unsupported or quarantined", counts.Unsupported))
	}
	if insecure > 0 {
		warnings = append(warnings, "TLS verification was disabled in an imported link; replace it with a valid certificate or pinnedPeerCertSha256")
	}
	if counts.Usable == 0 && (snap.State.Mode == contract.ModeTargeted || snap.State.Mode == contract.ModeProxyAll) {
		warnings = append(warnings, "pool has no usable exit; approved destinations fail with pool_unavailable; run 'pool refresh' or 'pool add <link>'")
	}
	if snap.State.SelectedExit != "" && !poolHas(p, snap.State.SelectedExit) {
		warnings = append(warnings, "the manually selected exit no longer exists; run 'pool select auto'")
	}
	if stopped {
		warnings = append(warnings, "service is stopped; traffic listeners are closed and leaving this mode needs a process restart")
	} else if !listenersOpen {
		warnings = append(warnings, "traffic listeners are not running; management is available but no traffic can be routed")
	}
	if snap.State.Desktop.Applied && !listenersOpen {
		warnings = append(warnings, "desktop proxy is recorded as applied but no listener is running; run 'desktop proxy restore'")
	}
	if snap.State.Mode == contract.ModeDirectAll {
		warnings = append(warnings, "all-direct mode: pool is unused and automatic probing is off")
	}
	if engineTransports > 0 {
		warnings = append(warnings, fmt.Sprintf("%d engine transport(s) are warm outside the registry; only a process restart clears them", engineTransports))
	}
	if warnings == nil {
		warnings = []string{}
	}
	for i := range warnings {
		warnings[i] = SanitizeTerminal(warnings[i])
	}
	return warnings
}

// ---------- doctor ----------

// Check is one doctor finding. Detail is factual; Fix is an actionable command
// or empty. No check claims a protection the service does not provide.
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok | warn | error
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// DoctorData is the `doctor` payload.
type DoctorData struct {
	Checks []Check `json:"checks"`
}

func (s *Service) doctor(ctx context.Context) (DoctorData, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	snap, err := s.store.Load(ctx)
	if err != nil {
		return DoctorData{}, withResidueNote(err, s.store.Residue())
	}
	paths := s.paths
	if paths.State == "" {
		paths = s.store.Paths()
	}
	var checks []Check
	add := func(name, status, detail, fix string) {
		checks = append(checks, Check{Name: name, Status: status, Detail: SanitizeTerminal(detail), Fix: SanitizeTerminal(fix)})
	}

	for _, item := range []struct {
		name string
		path string
		dir  bool
		perm os.FileMode
	}{
		{"state_dir", paths.StateDir, true, 0o700},
		{"state_file", paths.State, false, 0o600},
		{"config_dir", paths.ConfigDir, true, 0o700},
		{"runtime_dir", paths.RuntimeDir, true, 0o700},
		{"config_file", paths.Config, false, 0o600},
	} {
		info, err := os.Stat(item.path)
		if os.IsNotExist(err) {
			if item.name == "state_file" || item.name == "config_file" {
				add(item.name, "ok", "not created yet; defaults will be used", "")
			} else {
				add(item.name, "warn", "directory does not exist yet", "")
			}
			continue
		}
		if err != nil {
			add(item.name, "error", "cannot inspect: permission denied or unreadable", "check the path and its parent permissions")
			continue
		}
		want := item.perm
		if info.Mode().Perm() != want {
			fix := "chmod " + strconv.FormatUint(uint64(want.Perm()), 8) + " " + filepath.Base(item.path)
			add(item.name, "warn", fmt.Sprintf("mode is %04o, expected %04o", info.Mode().Perm(), want), fix)
		} else {
			add(item.name, "ok", fmt.Sprintf("mode %04o", info.Mode().Perm()), "")
		}
	}

	if info, err := os.Stat(paths.Socket); err == nil {
		mode := info.Mode().Perm()
		detail := fmt.Sprintf("socket mode %04o", mode)
		if info.Mode()&os.ModeSocket == 0 {
			add("management_socket", "error", "path exists but is not a Unix socket", "remove the stale path or choose another data dir")
		} else if mode&0o077 != 0 {
			add("management_socket", "warn", detail+"; group/other can open it", "chmod 600 "+filepath.Base(paths.Socket))
		} else if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
			add("management_socket", "warn", detail+"; owned by another uid", "inspect the runtime directory")
		} else {
			add("management_socket", "ok", detail+"; same-uid peer check is enforced (root and same-uid processes are inside the boundary)", "")
		}
	} else if os.IsNotExist(err) {
		if s.listenersOpen || s.stopped {
			add("management_socket", "warn", "management socket is not set up in this process", "")
		} else {
			add("management_socket", "warn", "service is not running", "start it with 'routelatch run'")
		}
	}

	listenerOK := isLoopbackListen(snap.Config.HTTPListen) && isLoopbackListen(snap.Config.SOCKSListen)
	if listenerOK {
		add("listeners", "ok", "both traffic listeners are literal loopback addresses: "+snap.Config.HTTPListen+", "+snap.Config.SOCKSListen, "")
	} else {
		add("listeners", "error", "listener configuration is not a loopback host:port pair", "set http_listen/socks_listen to 127.0.0.1 or ::1")
	}
	if s.listenersOpen {
		bound := isLoopbackListen(s.httpAddr) && isLoopbackListen(s.socksAddr)
		if bound {
			add("bound_listeners", "ok", "traffic listeners are bound on loopback", "")
		} else {
			add("bound_listeners", "error", "a bound listener is not loopback", "stop the service and check the listener configuration")
		}
	}

	p := pool.Build(snap.State, s.now().UTC())
	counts := p.Counts()
	switch {
	case counts.Total == 0:
		add("pool", "warn", "pool is empty; approved destinations fail with pool_unavailable", "routelatch pool refresh --all or routelatch pool add <link>")
	case counts.Usable == 0:
		add("pool", "warn", fmt.Sprintf("no usable exit (total %d, cooling %d, unsupported %d, invalid %d)", counts.Total, counts.Cooling, counts.Unsupported, counts.Invalid), "run 'pool test' or refresh the source")
	default:
		add("pool", "ok", fmt.Sprintf("%d usable of %d records", counts.Usable, counts.Total), "")
	}
	if counts.Unsupported > 0 || counts.Invalid > 0 {
		fix := "routelatch pool list"
		if hasInsecure(snap.State.Records) {
			fix = "replace skip-verify links with a valid certificate or pinnedPeerCertSha256"
		}
		add("quarantined_records", "warn", fmt.Sprintf("%d unsupported and %d invalid records are kept for reporting and never dialed", counts.Unsupported, counts.Invalid), fix)
	}
	if snap.State.SchemaVersion != contract.SchemaVersionCurrent {
		add("schema_version", "error", fmt.Sprintf("state schema %d, expected %d", snap.State.SchemaVersion, contract.SchemaVersionCurrent), "restore a compatible backup")
	} else {
		add("schema_version", "ok", fmt.Sprintf("state schema %d", snap.State.SchemaVersion), "")
	}

	residue := s.store.Residue()
	switch {
	case len(residue.Warnings) > 0:
		add("proposal_residue", "warn", strings.Join(residue.Warnings, "; "),
			"inspect the named file; RouteLatch leaves a file it cannot parse or verify untouched")
	case residue.Rewritten > 0:
		add("proposal_residue", "ok", fmt.Sprintf("legacy proposal history was removed from %d file(s) on open", residue.Rewritten), "")
	default:
		add("proposal_residue", "ok", "no legacy proposal history in state or backups", "")
	}

	logPath := s.logPath
	if logPath == "" {
		logPath = filepath.Join(paths.LogDir, "routelatch.log")
	}
	if err := logFileWritable(logPath); err != nil {
		add("log_file", "warn", "log file "+logPath+" is not writable: "+err.Error(), "check the log directory permissions; the service keeps serving without a file log")
	} else {
		add("log_file", "ok", "log file "+logPath+" is writable", "")
	}

	var conflicts []string
	if snap.State.Desktop.Applied && !s.listenersOpen {
		conflicts = append(conflicts, "desktop proxy is applied while no listener is running")
	}
	if snap.State.SelectedExit != "" && !poolHas(p, snap.State.SelectedExit) {
		conflicts = append(conflicts, "selected exit does not exist")
	}
	if snap.State.Mode == contract.ModeStopped {
		conflicts = append(conflicts, "state is stopped; a new process is needed to listen again")
	}
	if len(conflicts) > 0 {
		add("conflicting_state", "warn", strings.Join(conflicts, "; "), "routelatch desktop proxy restore (if applied) or review 'status'")
	} else {
		add("conflicting_state", "ok", "no conflicting persisted marker found", "")
	}
	return DoctorData{Checks: checks}, nil
}

func hasInsecure(records []contract.Record) bool {
	for _, rec := range records {
		if rec.InsecureTLS || rec.StatusReason == contract.ReasonInsecureTLSRemoved {
			return true
		}
	}
	return false
}

// ---------- env ----------

// EnvData is the launcher environment. It advertises only the two loopback
// listeners plus the fixed local/LAN NO_PROXY list.
type EnvData struct {
	HTTPProxy  string `json:"HTTP_PROXY"`
	HTTPSProxy string `json:"HTTPS_PROXY"`
	ALLProxy   string `json:"ALL_PROXY"`
	NoProxy    string `json:"NO_PROXY"`
	Notice     string `json:"notice"`
}

func (s *Service) env() (EnvData, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if !s.listenersOpen {
		return EnvData{}, &contract.Failure{Kind: contract.Stopped, Path: contract.PathLocal, Op: "control.env", Message: "service has no traffic listener; direct fallback is not offered"}
	}
	snap := s.Snapshot()
	targets := platform.Targets{HTTPListen: snap.Config.HTTPListen, SOCKSListen: snap.Config.SOCKSListen}
	if err := targets.Validate(); err != nil {
		return EnvData{}, err
	}
	env, err := platform.BuildProxyEnvironment(nil, targets)
	if err != nil {
		return EnvData{}, err
	}
	values := make(map[string]string, len(env))
	for _, entry := range env {
		if i := strings.IndexByte(entry, '='); i > 0 {
			values[entry[:i]] = entry[i+1:]
		}
	}
	return EnvData{
		HTTPProxy: values["HTTP_PROXY"], HTTPSProxy: values["HTTPS_PROXY"],
		ALLProxy: values["ALL_PROXY"], NoProxy: values["NO_PROXY"], Notice: platform.LaunchNotice,
	}, nil
}

// ---------- config ----------

// ConfigData is `config get` / `config set`.
type ConfigData struct {
	Path   string          `json:"path"`
	Values contract.Config `json:"values"`
	Notice string          `json:"notice,omitempty"`
}

func (s *Service) configGet() (ConfigData, error) {
	snap := s.Snapshot()
	return ConfigData{Path: SanitizeTerminal(s.paths.Config), Values: snap.Config}, nil
}

// ConfigPathData is `config path`.
type ConfigPathData struct {
	Config string `json:"config"`
	State  string `json:"state"`
	Socket string `json:"socket"`
}

func (s *Service) configPath() ConfigPathData {
	return ConfigPathData{Config: SanitizeTerminal(s.paths.Config), State: SanitizeTerminal(s.paths.State), Socket: SanitizeTerminal(s.paths.Socket)}
}

func (s *Service) configSet(ctx context.Context, key, value string) (ConfigData, error) {
	if key == "" {
		return ConfigData{}, usage("config set needs a key and a value")
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	snap, err := s.store.MutateConfig(ctx, func(cfg *contract.Config) error {
		return applyConfigValue(cfg, key, value)
	})
	if err != nil {
		return ConfigData{}, err
	}
	s.snap.Store(&snap)
	s.logger.SetLevel(snap.Config.LogLevel)
	notice := ""
	switch key {
	case "log_level", "probes.targets", "probes.timeout_ms", "probes.cooldown_base", "probes.cooldown_max":
		notice = key + " is stored but the running engine/prober keeps its startup value until a restart"
	}
	if s.diag != nil {
		if snap.Config.Diagnostics.Enabled {
			s.diag.Enable()
		} else {
			_, _ = s.diag.Disable(ctx)
		}
	}
	return ConfigData{Path: SanitizeTerminal(s.paths.Config), Values: snap.Config, Notice: notice}, nil
}

func applyConfigValue(cfg *contract.Config, key, value string) error {
	switch key {
	case "http_listen", "socks_listen":
		if !isLoopbackListen(value) {
			// D14 / §4.2: a listener that is not literal loopback is outside
			// v1, not a malformed command line.
			return &contract.Failure{
				Kind: contract.Unsupported, Path: contract.PathLocal, Op: "config.set",
				Message: "listener must be a literal loopback host:port (127.0.0.1 or ::1); other listeners are unsupported",
			}
		}
		if key == "http_listen" {
			cfg.HTTPListen = value
		} else {
			cfg.SOCKSListen = value
		}
	case "log_level":
		switch value {
		case "debug", "info", "warning", "error", "none":
			cfg.LogLevel = value
		default:
			return usage("log_level must be debug, info, warning, error or none")
		}
	case "diagnostics.enabled":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return usage("diagnostics.enabled must be true or false")
		}
		cfg.Diagnostics.Enabled = b
	case "diagnostics.queue_limit":
		n, err := positiveInt(value)
		if err != nil {
			return usage("diagnostics.queue_limit must be a positive integer")
		}
		cfg.Diagnostics.QueueLimit = n
	case "diagnostics.max_concurrent":
		n, err := positiveInt(value)
		if err != nil {
			return usage("diagnostics.max_concurrent must be a positive integer")
		}
		cfg.Diagnostics.MaxConcurrent = n
	case "diagnostics.retention_days":
		n, err := positiveInt(value)
		if err != nil {
			return usage("diagnostics.retention_days must be a positive integer")
		}
		cfg.Diagnostics.RetentionDays = n
	case "probes.timeout_ms":
		n, err := positiveInt(value)
		if err != nil {
			return usage("probes.timeout_ms must be a positive integer")
		}
		cfg.Probes.TimeoutMS = n
	case "probes.cooldown_base":
		n, err := positiveInt(value)
		if err != nil {
			return usage("probes.cooldown_base must be a positive integer (seconds)")
		}
		cfg.Probes.CooldownBase = n
	case "probes.cooldown_max":
		n, err := positiveInt(value)
		if err != nil {
			return usage("probes.cooldown_max must be a positive integer (seconds)")
		}
		cfg.Probes.CooldownMax = n
	case "probes.targets":
		var targets []string
		if strings.TrimSpace(value) != "" {
			for _, raw := range strings.Split(value, ",") {
				raw = strings.TrimSpace(raw)
				if raw == "" {
					continue
				}
				if _, ok := probes.ParseTarget(raw); !ok {
					return usage("probes.targets must be comma-separated host:port values")
				}
				targets = append(targets, raw)
			}
		}
		cfg.Probes.Targets = targets
	default:
		return usage("unknown config key; use http_listen, socks_listen, log_level, diagnostics.* or probes.*")
	}
	cfg.SchemaVersion = contract.SchemaVersionCurrent
	return nil
}

func positiveInt(value string) (int, error) {
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("not a positive integer")
	}
	return n, nil
}

func usage(message string) error {
	return &OpError{Kind: contract.InvalidDestination, Message: message, Exit: contract.ExitUsage}
}

func poolHas(p *pool.Pool, exit contract.ExitID) bool {
	_, ok := p.Record(exit)
	return ok
}
