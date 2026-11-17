package contract

import (
	"context"
	"time"
)

// SchemaVersion is the on-disk format version. It is independent of the
// application version and only moves with a migration (docs/contracts.md §4.5).
type SchemaVersion int

// SchemaVersionCurrent is the only version this build writes.
const SchemaVersionCurrent SchemaVersion = 1

// Config is the read-mostly configuration file (config.json). Changing a
// listener address requires a service restart; the service never rebinds a
// traffic listener in place.
type Config struct {
	SchemaVersion SchemaVersion     `json:"schema_version"`
	HTTPListen    string            `json:"http_listen"`
	SOCKSListen   string            `json:"socks_listen"`
	LogLevel      string            `json:"log_level"`
	Diagnostics   DiagnosticsConfig `json:"diagnostics"`
	Probes        ProbeConfig       `json:"probes"`
}

// DiagnosticsConfig is user-controlled automatic diagnosis (D09). Enabled
// false stops new proposals and cancels the queue; manual diagnosis and pool
// health remain separate commands.
type DiagnosticsConfig struct {
	Enabled       bool `json:"enabled"`
	QueueLimit    int  `json:"queue_limit"`
	MaxConcurrent int  `json:"max_concurrent"`
	RetentionDays int  `json:"retention_days"`
}

// ProbeConfig holds the explicit targets a health probe may use. An empty
// list means "transport and passive signals only"; RouteLatch never invents a
// third-party target (docs/contracts.md §6.1).
type ProbeConfig struct {
	Targets      []string `json:"targets"` // "host:port"; egress probes send the hostname unresolved
	TimeoutMS    int      `json:"timeout_ms"`
	CooldownBase int      `json:"cooldown_base"` // seconds; doubled per consecutive transport failure
	CooldownMax  int      `json:"cooldown_max"`  // seconds
}

// State is the mutable file (state.json). It is written only through
// Store.Mutate and read only through Store.Load.
type State struct {
	SchemaVersion SchemaVersion `json:"schema_version"`
	PolicyVersion PolicyVersion `json:"policy_version"`
	Mode          Mode          `json:"mode"`
	// SelectedExit pins one exit for new pool dials. Empty means automatic
	// ranking; a pinned exit that fails is an error, never a silent switch.
	SelectedExit ExitID            `json:"selected_exit,omitempty"`
	Rules        []Rule            `json:"rules"`
	Sources      []Source          `json:"sources"`
	Records      []Record          `json:"records"`
	Health       []ExitHealth      `json:"health"`
	Proposals    []Proposal        `json:"proposals"`
	Desktop      DesktopProxyState `json:"desktop"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

// DesktopProxyState is the ownership marker for the optional desktop proxy
// setting (D19). Each key records both the value RouteLatch applied and the
// value it replaced, so restore can revert only the keys that still hold our
// value and leave the user's later edits alone.
type DesktopProxyState struct {
	Applied     bool         `json:"applied"`
	Environment string       `json:"environment,omitempty"` // "gnome" | "kde"
	AppliedAt   time.Time    `json:"applied_at,omitzero"`
	InstanceID  string       `json:"instance_id,omitempty"`
	Keys        []DesktopKey `json:"keys,omitempty"`
}

// DesktopKey is one setting RouteLatch wrote.
type DesktopKey struct {
	Name     string `json:"name"`     // e.g. "org.gnome.system.proxy mode"
	Applied  string `json:"applied"`  // what we wrote
	Previous string `json:"previous"` // what was there before
}

// Snapshot is one consistent view of both files.
type Snapshot struct {
	Config Config `json:"config"`
	State  State  `json:"state"`
}

// BackupRef names one backup accepted by Restore.
type BackupRef struct {
	Path          string        `json:"path"`
	SchemaVersion SchemaVersion `json:"schema_version"`
	CreatedAt     time.Time     `json:"created_at"`
	Checksum      string        `json:"checksum"` // "sha256:<hex>" over the backup payload
}

// Store is the only component allowed to read or replace the persisted files.
//
// Invariants:
//   - There is one writer. Mutate and MutateConfig take the repository lock for
//     the whole read-modify-write and fail with Kind StateLocked rather than
//     waiting forever. A CLI process uses them only when the service is not
//     running (offline mode); otherwise it sends the mutation to the management
//     socket.
//   - Each call either commits the whole change to the file it owns or leaves
//     that file unchanged. It writes a same-directory temp file with mode 0600,
//     fsyncs the file, renames it over the target, and fsyncs the directory
//     before returning. Config and state are separate files and are not
//     promised to change atomically together.
//   - Any mutation that changes Mode, Rules or SelectedExit must leave
//     PolicyVersion strictly greater than before; Mutate rejects a callback
//     that does not. A listener change in MutateConfig takes effect only on the
//     next service start.
//   - Load migrates an older schema forward (backup first) and refuses a newer
//     schema with Kind StateCorrupt. It never rewrites a file it could not
//     parse.
//   - Backup writes one envelope: {schema_version, created_at, checksum, state}
//     and Restore validates the checksum, schema and that the path stays inside
//     the backup directory before replacing the live file. A failed restore
//     changes nothing.
//   - No method logs or returns a credential, subscription URL, or the contents
//     of a share link.
type Store interface {
	Load(ctx context.Context) (Snapshot, error)
	Mutate(ctx context.Context, fn func(*State) error) (Snapshot, error)
	MutateConfig(ctx context.Context, fn func(*Config) error) (Snapshot, error)
	Backup(ctx context.Context) (BackupRef, error)
	Restore(ctx context.Context, ref BackupRef) (Snapshot, error)
}
