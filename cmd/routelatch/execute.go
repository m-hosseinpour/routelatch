package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"routelatch/internal/autostart"
	"routelatch/internal/contract"
	"routelatch/internal/control"
	"routelatch/internal/platform"
	"routelatch/internal/service"
	"routelatch/internal/store"
)

type cli struct {
	jsonOut    bool
	dataDir    string
	dataDirSet bool
	stdout     io.Writer
	stderr     io.Writer
	paths      store.Paths
}

func main() {
	code := Execute(context.Background(), os.Args[1:], os.Stdout, os.Stderr)
	os.Exit(code)
}

// Execute parses global flags, dispatches one command and returns its documented
// exit status. It is exported inside package main so tests can run commands
// against an in-process management server.
func Execute(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	c := &cli{stdout: stdout, stderr: stderr}
	rest, err := c.parseGlobal(args)
	if err != nil {
		return c.localFailure("", err)
	}
	if len(rest) == 0 {
		c.usage()
		return int(contract.ExitUsage)
	}
	// Version reporting must work without a service, socket or data directory.
	if rest[0] == "version" {
		if len(rest) != 1 {
			return c.localFailure("version", usageErr("version takes no arguments"))
		}
		return c.versionCommand()
	}
	// The privileged TUN service is a unit entrypoint, not a user command. It
	// owns no instance paths, and it must work without a login home directory.
	if rest[0] == "__tun-service" {
		return c.tunService(ctx, rest[1:])
	}
	paths, err := store.ResolvePaths(c.dataDir)
	if err != nil {
		return c.localFailure(rest[0], err)
	}
	c.paths = paths
	return c.dispatch(ctx, rest)
}

// parseGlobal removes --json and --data-dir from anywhere before a literal
// "--" so a per-command parser never sees them.
func (c *cli) parseGlobal(args []string) ([]string, error) {
	var rest []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		switch {
		case arg == "--json" || arg == "-json":
			c.jsonOut = true
		case arg == "--data-dir":
			if i+1 >= len(args) {
				return nil, usageErr("--data-dir needs a value")
			}
			c.dataDir = args[i+1]
			c.dataDirSet = true
			i++
		case strings.HasPrefix(arg, "--data-dir="):
			c.dataDir = strings.TrimPrefix(arg, "--data-dir=")
			c.dataDirSet = true
		default:
			rest = append(rest, arg)
		}
	}
	return rest, nil
}

func (c *cli) dispatch(ctx context.Context, args []string) int {
	cmd := args[0]
	rest := args[1:]
	switch cmd {
	case "help", "--help", "-h":
		c.usage()
		return 0
	case "run":
		return c.run(ctx, rest)
	case "__app-supervisor":
		return c.appSupervisor(ctx)
	case "__app-tray":
		return c.appTray()
	case "menu":
		return c.menu(ctx)
	case "status":
		return c.call(ctx, control.Request{Op: control.OpStatus})
	case "mode":
		return c.mode(ctx, rest)
	case "rules":
		return c.rules(ctx, rest)
	case "pool":
		return c.pool(ctx, rest)
	case "import":
		return c.importCmd(ctx, rest)
	case "diagnose":
		return c.diagnose(ctx, rest)
	case "proposals":
		return c.proposals(ctx, rest)
	case "config":
		return c.config(ctx, rest)
	case "doctor":
		return c.doctor(ctx)
	case "autostart":
		return c.autostart(ctx, rest)
	case "service":
		return c.serviceCmd(ctx, rest)
	case "env":
		return c.call(ctx, control.Request{Op: control.OpEnv})
	case "exec":
		return c.exec(ctx, rest)
	case "desktop":
		return c.desktop(ctx, rest)
	case "backup":
		return c.backup(ctx)
	case "restore":
		if len(rest) != 1 {
			return c.localFailure("restore", usageErr("restore needs one backup path"))
		}
		return c.restore(ctx, rest[0])
	default:
		return c.localFailure(cmd, usageErr("unknown command: "+cmd))
	}
}

func (c *cli) client() *control.Client {
	return &control.Client{Path: c.paths.Socket}
}

// backup and restore fall back to the offline store when no service is
// running. The CLI then owns the single-writer lock for that call (§4.4).
func (c *cli) backup(ctx context.Context) int {
	env, err := c.client().Call(ctx, control.Request{Op: control.OpBackup})
	if err == nil {
		return c.print(env)
	}
	if !errors.Is(err, control.ErrNotRunning) {
		return c.localFailure("backup", err)
	}
	st, lock, code := c.openOffline(ctx, "backup")
	if code != -1 {
		return code
	}
	defer lock.Release()
	ref, err := st.Backup(ctx)
	return c.print(control.EnvelopeFor("backup", map[string]any{"ref": ref}, err))
}

func (c *cli) restore(ctx context.Context, ref string) int {
	env, err := c.client().Call(ctx, control.Request{Op: control.OpRestore, Ref: ref})
	if err == nil {
		return c.print(env)
	}
	if !errors.Is(err, control.ErrNotRunning) {
		return c.localFailure("restore", err)
	}
	st, lock, code := c.openOffline(ctx, "restore")
	if code != -1 {
		return code
	}
	defer lock.Release()
	snap, err := st.RestorePath(ctx, ref)
	data := map[string]any{"mode": snap.State.Mode, "policy_version": snap.State.PolicyVersion, "closed": 0}
	return c.print(control.EnvelopeFor("restore", data, err))
}

func (c *cli) desktopRestoreOffline(ctx context.Context) (int, bool) {
	st, lock, code := c.openOffline(ctx, "desktop proxy restore")
	if code != -1 || st == nil {
		return code, true
	}
	defer lock.Release()
	snap, err := st.Load(ctx)
	if err != nil {
		return c.print(control.EnvelopeFor("desktop proxy restore", nil, err)), true
	}
	if !snap.State.Desktop.Applied && len(snap.State.Desktop.Keys) == 0 {
		return c.print(control.EnvelopeFor("desktop proxy restore", control.DesktopData{Notice: "no desktop proxy setting was applied by RouteLatch"}, nil)), true
	}
	adapter := &platform.DesktopAdapter{Runner: platform.ExecRunner{}, Now: time.Now}
	result, err := adapter.Restore(ctx, snap.State.Desktop)
	if err != nil {
		return c.print(control.EnvelopeFor("desktop proxy restore", nil, err)), true
	}
	if _, err := st.Mutate(ctx, func(state *contract.State) error {
		state.Desktop = result.State
		return nil
	}); err != nil {
		return c.print(control.EnvelopeFor("desktop proxy restore", nil, err)), true
	}
	data := control.DesktopData{
		Environment: snap.State.Desktop.Environment, Supported: true,
		Applied: result.State.Applied, PreviousSet: len(result.State.Keys) > 0,
		Restored: result.Restored, UserModified: result.UserModified, Failed: result.Failed,
	}
	return c.print(control.EnvelopeFor("desktop proxy restore", data, nil)), true
}

// openOffline opens the store and takes the instance lock. code is -1 when the
// caller may use st; otherwise it is the already-printed exit status.
func (c *cli) openOffline(ctx context.Context, command string) (*store.Store, *store.Lock, int) {
	st, err := store.Open(store.Options{Paths: c.paths})
	if err != nil {
		return nil, nil, c.localFailure(command, err)
	}
	lock, err := st.Acquire(ctx)
	if err != nil {
		return nil, nil, c.localFailure(command, err)
	}
	return st, lock, -1
}

// doctor falls back to an offline check when no service is running. A corrupt
// state still produces the documented exit 10 instead of "not running", and
// the offline path never touches the network. Opening the store may rewrite
// legacy proposal residue (D21), so this command is not strictly read-only.
func (c *cli) doctor(ctx context.Context) int {
	env, err := c.client().Call(ctx, control.Request{Op: control.OpDoctor})
	if err == nil {
		return c.print(env)
	}
	if !errors.Is(err, control.ErrNotRunning) {
		return c.localFailure("doctor", err)
	}
	st, openErr := store.Open(store.Options{Paths: c.paths, AllowInsecure: true})
	if openErr != nil {
		return c.localFailure("doctor", openErr)
	}
	lock, lockErr := st.Acquire(ctx)
	if lockErr != nil {
		return c.localFailure("doctor", lockErr)
	}
	defer lock.Release()
	svc, svcErr := control.NewService(ctx, control.Options{
		Store: st, Paths: c.paths, LogOutput: io.Discard,
		TrayProbe: appTrayProbe, PIDFDProbe: appPIDFDProbe,
	})
	if svcErr != nil {
		return c.localFailure("doctor", svcErr)
	}
	data, handleErr := svc.Handle(ctx, control.Request{Op: control.OpDoctor})
	return c.print(control.EnvelopeFor("doctor", data, handleErr))
}

// call sends one request and prints either the JSON envelope or human output.
func (c *cli) call(ctx context.Context, req control.Request) int {
	env, err := c.client().Call(ctx, req)
	if err != nil {
		if errors.Is(err, control.ErrNotRunning) {
			return c.localFailure(control.CommandName(req.Op), &contract.Failure{
				Kind: contract.Stopped, Path: contract.PathLocal, Op: "cli",
				Message: "RouteLatch is not running (management socket is absent); start it with 'routelatch run'",
			})
		}
		return c.localFailure(control.CommandName(req.Op), err)
	}
	return c.print(env)
}

func (c *cli) print(env contract.Envelope) int {
	if c.jsonOut {
		enc := json.NewEncoder(c.stdout)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(env); err != nil {
			fmt.Fprintln(c.stderr, "error: cannot write JSON output")
			return int(contract.ExitError)
		}
	} else {
		c.human(env)
	}
	return int(control.ExitCodeForEnvelope(env))
}

func (c *cli) human(env contract.Envelope) {
	if !env.OK {
		if env.Error != nil {
			fmt.Fprintln(c.stderr, "error:", env.Error.Message)
			if len(env.Error.Detail) > 0 {
				if ref, ok := env.Error.Detail["source_id"]; ok {
					fmt.Fprintln(c.stderr, "source:", ref)
				}
			}
		} else {
			fmt.Fprintln(c.stderr, "error: command failed")
		}
		return
	}
	switch env.Command {
	case "status":
		if d, ok := env.Data.(map[string]any); ok {
			c.printStatus(d)
			return
		}
	case "mode":
		if d, ok := env.Data.(map[string]any); ok {
			fmt.Fprintf(c.stdout, "mode: %v (policy %v)\n", d["mode"], d["policy_version"])
			if closed, ok := d["closed"].(map[string]any); ok {
				fmt.Fprintf(c.stdout, "closed: %v live, %v pending; kept: %v; %vms\n",
					closed["count"], closed["pending"], d["kept"], d["duration_ms"])
			}
			return
		}
	case "rules list":
		if d, ok := env.Data.(map[string]any); ok {
			if rules, ok := d["rules"].([]any); ok {
				if len(rules) == 0 {
					fmt.Fprintln(c.stdout, "no approved destinations")
				}
				for _, item := range rules {
					if r, ok := item.(map[string]any); ok {
						fmt.Fprintf(c.stdout, "%v  %v  %v  %v\n", r["id"], r["kind"], ruleOperandFromJSON(r), r["note"])
					}
				}
			}
			return
		}
	case "rules add":
		if d, ok := env.Data.(map[string]any); ok {
			if r, ok := d["rule"].(map[string]any); ok {
				fmt.Fprintf(c.stdout, "approved %v (%v)\n", ruleOperandFromJSON(r), r["id"])
			}
			if notice, _ := d["notice"].(string); notice != "" {
				fmt.Fprintln(c.stderr, notice)
			}
			return
		}
	case "rules remove", "pool remove":
		if d, ok := env.Data.(map[string]any); ok {
			fmt.Fprintf(c.stdout, "removed %v; closed %v connection(s)\n", firstNonEmpty(d["removed"]), d["closed"])
			return
		}
	case "pool list":
		if d, ok := env.Data.(map[string]any); ok {
			records, _ := d["records"].([]any)
			if len(records) == 0 {
				fmt.Fprintln(c.stdout, "pool is empty; run 'pool refresh --all' or 'pool add <link>'")
			}
			for _, item := range records {
				if r, ok := item.(map[string]any); ok {
					fmt.Fprintf(c.stdout, "%v  %v  %v  %v:%v  %v/%v  %v\n",
						r["id"], r["status"], r["protocol"], r["server"], r["port"], r["transport"], r["security"], r["display_name"])
				}
			}
			if counts, ok := d["counts"].(map[string]any); ok {
				fmt.Fprintf(c.stdout, "total %v, usable %v, healthy %v, cooling %v\n", counts["total"], counts["usable"], counts["healthy"], counts["cooling"])
			}
			return
		}
	case "pool refresh":
		if d, ok := env.Data.(map[string]any); ok {
			if sources, ok := d["sources"].([]any); ok {
				for _, item := range sources {
					if s, ok := item.(map[string]any); ok {
						fmt.Fprintf(c.stdout, "%v ok=%v records=%v %v\n", s["id"], s["ok"], s["records"], s["error"])
					}
				}
			}
			return
		}
	case "pool test":
		if d, ok := env.Data.(map[string]any); ok {
			if results, ok := d["results"].([]any); ok {
				for _, item := range results {
					if r, ok := item.(map[string]any); ok {
						fmt.Fprintf(c.stdout, "%v  %v  %v  %vms  %v bytes\n", r["exit"], r["kind"], r["outcome"], r["duration_ms"], r["bytes_down"])
					}
				}
			}
			return
		}
	case "pool add", "import":
		if d, ok := env.Data.(map[string]any); ok {
			fmt.Fprintf(c.stdout, "source %v: %v valid, %v invalid, %v unsupported\n", d["source_id"], nested(d, "records", "valid"), nested(d, "records", "invalid"), nested(d, "records", "unsupported"))
			if notices, ok := d["notices"].([]any); ok {
				for _, n := range notices {
					fmt.Fprintln(c.stderr, n)
				}
			}
			return
		}
	case "pool select":
		if d, ok := env.Data.(map[string]any); ok {
			fmt.Fprintf(c.stdout, "selected: %v (policy %v)\n", firstNonEmpty(d["selected"]), d["policy_version"])
			return
		}
	case "pool rerank":
		if d, ok := env.Data.(map[string]any); ok {
			before, _ := d["selected_before"].(string)
			after, _ := d["selected_after"].(string)
			fmt.Fprintf(c.stdout, "selected before: %v\nselected after: %v\n", displayExit(before), displayExit(after))
			if before == after && before != "" {
				fmt.Fprintln(c.stdout, "selection unchanged")
			}
			if skipped, ok := d["skipped"].([]any); ok {
				pinned := false
				for _, item := range skipped {
					if s, ok := item.(map[string]any); ok {
						if s["reason"] == "pinned" {
							pinned = true
						}
						fmt.Fprintf(c.stdout, "skipped: %v (%v)\n", s["exit"], s["reason"])
					}
				}
				if pinned {
					fmt.Fprintln(c.stdout, "selection is pinned; the manual selection is unchanged")
				}
				if len(skipped) == 0 {
					fmt.Fprintln(c.stdout, "skipped: none")
				}
			}
			fmt.Fprintf(c.stdout, "policy version: %v\n", d["policy_version"])
			return
		}
	case "diagnose":
		if d, ok := env.Data.(map[string]any); ok {
			fmt.Fprintf(c.stdout, "diagnose %v (%v)\n", d["destination"], d["kind"])
			if attempts, ok := d["attempts"].([]any); ok {
				for _, item := range attempts {
					if a, ok := item.(map[string]any); ok {
						fmt.Fprintf(c.stdout, "  %v: %v %vms\n", a["kind"], a["outcome"], a["duration_ms"])
					}
				}
			}
			fmt.Fprintf(c.stdout, "eligible: %v", d["eligible"])
			if id, ok := d["proposal_id"].(string); ok && id != "" {
				fmt.Fprintf(c.stdout, "  proposal: %v", id)
			}
			fmt.Fprintln(c.stdout)
			return
		}
	case "proposals list":
		if d, ok := env.Data.(map[string]any); ok {
			if list, ok := d["proposals"].([]any); ok {
				if len(list) == 0 {
					fmt.Fprintln(c.stdout, "no proposals")
				}
				for _, item := range list {
					if p, ok := item.(map[string]any); ok {
						fmt.Fprintf(c.stdout, "%v  %v  %v  %v\n", p["id"], p["status"], p["destination"], p["expires_at"])
					}
				}
			}
			return
		}
	case "proposals accept":
		if d, ok := env.Data.(map[string]any); ok {
			fmt.Fprintf(c.stdout, "accepted proposal; rule %v\n", d["rule_id"])
			return
		}
	case "config get", "config set":
		if d, ok := env.Data.(map[string]any); ok {
			fmt.Fprintf(c.stdout, "path: %v\n", d["path"])
			if notice, _ := d["notice"].(string); notice != "" {
				fmt.Fprintln(c.stderr, notice)
			}
			if values, ok := d["values"].(map[string]any); ok {
				for _, key := range []string{"http_listen", "socks_listen", "log_level"} {
					fmt.Fprintf(c.stdout, "%v: %v\n", key, values[key])
				}
			}
			return
		}
	case "config path":
		if d, ok := env.Data.(map[string]any); ok {
			fmt.Fprintf(c.stdout, "config: %v\nstate: %v\nsocket: %v\n", d["config"], d["state"], d["socket"])
			return
		}
	case "doctor":
		if d, ok := env.Data.(map[string]any); ok {
			if checks, ok := d["checks"].([]any); ok {
				for _, item := range checks {
					if ch, ok := item.(map[string]any); ok {
						fmt.Fprintf(c.stdout, "[%v] %v: %v", ch["status"], ch["name"], ch["detail"])
						if fix, _ := ch["fix"].(string); fix != "" {
							fmt.Fprintf(c.stdout, "  (%v)", fix)
						}
						fmt.Fprintln(c.stdout)
					}
				}
			}
			return
		}
	case "env":
		if d, ok := env.Data.(map[string]any); ok {
			for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY"} {
				fmt.Fprintf(c.stdout, "%v=%v\n", key, d[key])
			}
			if notice, _ := d["notice"].(string); notice != "" {
				fmt.Fprintln(c.stderr, notice)
			}
			return
		}
	case "backup":
		if d, ok := env.Data.(map[string]any); ok {
			if ref, ok := d["ref"].(map[string]any); ok {
				fmt.Fprintf(c.stdout, "backup: %v (%v)\n", ref["path"], ref["checksum"])
			}
			return
		}
	case "restore":
		if d, ok := env.Data.(map[string]any); ok {
			fmt.Fprintf(c.stdout, "restored mode %v at policy %v; closed %v connection(s)\n", d["mode"], d["policy_version"], d["closed"])
			return
		}
	case "desktop proxy status":
		if d, ok := env.Data.(map[string]any); ok {
			fmt.Fprintf(c.stdout, "environment: %v supported: %v applied: %v previous recorded: %v\n",
				d["environment"], d["supported"], d["applied"], d["previous_set"])
			if conflicts, ok := d["conflict"].([]any); ok && len(conflicts) > 0 {
				fmt.Fprintf(c.stdout, "conflict: changed outside RouteLatch: %v\n", conflicts)
			}
			if notice, ok := d["notice"].(string); ok && notice != "" {
				fmt.Fprintln(c.stdout, "notice:", notice)
			}
			return
		}
	case "desktop proxy set":
		if d, ok := env.Data.(map[string]any); ok {
			fmt.Fprintf(c.stdout, "desktop proxy applied for %v\n", d["environment"])
			return
		}
	case "desktop proxy restore":
		if d, ok := env.Data.(map[string]any); ok {
			fmt.Fprintf(c.stdout, "desktop proxy restored; modified by user: %v\n", d["user_modified"])
			return
		}
	case "autostart status", "autostart enable", "autostart disable":
		if d, ok := env.Data.(map[string]any); ok {
			switch env.Command {
			case "autostart enable":
				fmt.Fprintf(c.stdout, "autostart entry enabled: %v\n", d["path"])
			case "autostart disable":
				fmt.Fprintf(c.stdout, "autostart entry disabled: %v\n", d["path"])
			default:
				fmt.Fprintf(c.stdout, "autostart entry: %v\n", d["path"])
			}
			fmt.Fprintf(c.stdout, "enabled: %v\nmanaged: %v\n", yesNo(d["enabled"]), yesNo(d["managed"]))
			fmt.Fprintf(c.stdout, "tray available: %v\npidfd available: %v\n",
				yesNo(d["tray_available"]), yesNo(d["pidfd_available"]))
			if d["conflict"] == autostart.ConflictService {
				fmt.Fprintln(c.stderr, "warning: a managed RouteLatch service unit exists; disable one of autostart and the service")
			}
			return
		}
	case "service install", "service uninstall", "service status":
		if d, ok := env.Data.(map[string]any); ok {
			switch env.Command {
			case "service install":
				fmt.Fprintf(c.stdout, "service installed: %v\n", d["unit"])
			case "service uninstall":
				fmt.Fprintf(c.stdout, "service uninstalled: %v\n", d["unit"])
			default:
				fmt.Fprintf(c.stdout, "service unit: %v\n", d["unit"])
			}
			fmt.Fprintf(c.stdout, "enabled: %v\nactive: %v\nmanaged: %v\n",
				yesNo(d["enabled"]), yesNo(d["active"]), yesNo(d["managed"]))
			fmt.Fprintf(c.stdout, "linger: %v\n", d["linger"])
			if d["conflict"] == service.ConflictAutostart {
				fmt.Fprintln(c.stderr, "warning: a managed autostart entry exists; disable one of autostart and the service")
			}
			return
		}
	}
	// Fallback: print the data as indented JSON on stdout.
	raw, _ := json.MarshalIndent(env.Data, "", "  ")
	fmt.Fprintln(c.stdout, string(raw))
}

func (c *cli) printStatus(d map[string]any) {
	fmt.Fprintf(c.stdout, "mode: %v (policy %v)\n", d["mode"], d["policy_version"])
	// An older running service does not send selected_exit. Absence must not be
	// printed as "auto": that service may hold a manual pin, and claiming
	// automatic ranking is exactly the misreading this line exists to prevent.
	selected, present := d["selected_exit"].(string)
	switch {
	case !present:
		selected = "unknown (the running service does not report selected_exit; restart it after upgrading)"
	case selected == "":
		selected = "auto"
	default:
		selected = control.SanitizeTerminal(selected)
	}
	fmt.Fprintf(c.stdout, "exit: %s\n", selected)
	switch {
	case d["ready"] == true:
		fmt.Fprintln(c.stdout, "ready: yes")
	case d["ready"] == false:
		fmt.Fprintln(c.stdout, "ready: no")
	}
	if l, ok := d["listeners"].(map[string]any); ok {
		suffix := ""
		if open, present := d["listeners_open"].(bool); present && !open {
			suffix = " (closed)"
		}
		fmt.Fprintf(c.stdout, "listeners: http %v, socks %v%s\n", l["http"], l["socks"], suffix)
	}
	if p, ok := d["pool"].(map[string]any); ok {
		fmt.Fprintf(c.stdout, "pool: %v total, %v usable, %v healthy, %v cooling\n", p["total"], p["usable"], p["healthy"], p["cooling"])
	}
	if cn, ok := d["connections"].(map[string]any); ok {
		fmt.Fprintf(c.stdout, "connections: %v open, %v dialing\n", cn["open"], cn["dialing"])
	}
	if diag, ok := d["diagnostics"].(map[string]any); ok {
		fmt.Fprintf(c.stdout, "diagnostics: enabled=%v queued=%v\n", diag["enabled"], diag["queued"])
	}
	if warnings, ok := d["warnings"].([]any); ok {
		for _, w := range warnings {
			fmt.Fprintln(c.stderr, "warning:", w)
		}
	}
}

func nested(m map[string]any, keys ...string) any {
	var cur any = m
	for _, key := range keys {
		next, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = next[key]
	}
	return cur
}

func ruleOperandFromJSON(r map[string]any) any {
	for _, key := range []string{"host", "addr", "prefix"} {
		if v, ok := r[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func displayExit(id string) string {
	if id == "" {
		return "(none)"
	}
	return id
}

func firstNonEmpty(v any) any {
	if s, ok := v.(string); ok && s == "" {
		return "auto"
	}
	return v
}

func yesNo(v any) string {
	if v == true {
		return "yes"
	}
	return "no"
}

func (c *cli) localFailure(command string, err error) int {
	env := control.EnvelopeFor(command, nil, err)
	if c.jsonOut {
		enc := json.NewEncoder(c.stdout)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(env)
	} else if env.Error != nil {
		fmt.Fprintln(c.stderr, "error:", env.Error.Message)
	}
	code := control.ExitCodeForEnvelope(env)
	// A second instance is documented as exit 4 for `run`, while the kind table
	// maps StateLocked to conflict (6); the run command overrides that below.
	return int(code)
}

func (c *cli) usage() {
	fmt.Fprintln(c.stderr, `RouteLatch — local proxy with an explicit approved-destination list

Usage: routelatch [--json] [--data-dir DIR] <command> [args]

  run                       start the detached app and tray (returns after readiness)
  run --headless            start the engine in the foreground (terminal, SSH, service)
  version                   show build version, commit and date
  status                    show mode, listeners, pool and connections
  mode <mode>               targeted | all-proxy | all-direct | stopped
  rules list|add|remove     manage approved destinations
  pool list|add|remove|refresh|test|select|rerank
  import link|file|subscription|v2rayn <input>
  diagnose <host:port>      bounded direct diagnosis
  proposals list|accept|reject|recheck|clear
  config get|set|path
  doctor                    permissions, listeners, pool and state checks
  autostart enable|disable|status
                            start the app at desktop login (XDG autostart)
  service install|uninstall|status
                            opt-in systemd user service (run --headless)
  env | exec -- <cmd>       launcher environment
  desktop proxy status|set|restore
  backup | restore <ref>
  menu                      interactive command shell

Every command accepts --json and prints one Envelope with schema_version 1.`)
}

func usageErr(message string) error {
	return &control.OpError{Kind: contract.InvalidDestination, Message: message, Exit: contract.ExitUsage}
}

func (c *cli) newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}
