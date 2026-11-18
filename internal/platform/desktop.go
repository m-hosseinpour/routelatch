package platform

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"routelatch/internal/contract"
)

// DesktopAdapter applies and restores the optional desktop proxy setting. The
// zero value is not usable: Runner and (optionally) LookPath, Now must be set.
// Apply also requires Health, because the desktop must never be pointed at a
// listener the running service does not own; Restore never consults Health and
// works offline.
type DesktopAdapter struct {
	Runner   CommandRunner
	LookPath LookPathFunc
	Health   HealthChecker
	Now      func() time.Time
}

// ApplyOptions is an explicit opt-in. Kind is never detected inside Apply;
// desktop detection is a separate advisory helper, so a missed or spoofed
// environment variable cannot silently change a desktop setting.
type ApplyOptions struct {
	Kind    Kind
	Targets Targets
	// Previous is the state currently recorded in the store. Apply refuses to
	// overwrite an ownership marker for another environment, and is a no-op if
	// the same environment is already recorded.
	Previous contract.DesktopProxyState
	// Save, when set, is called after previous values are captured and before
	// any desktop key is written. The caller must persist the marker so a crash
	// during the write sequence still leaves enough information for an offline
	// restore. A Save error aborts Apply before it changes a setting. After
	// Apply returns, the caller persists the returned marker as well (normally
	// the same values; zero after a clean rollback; a subset after an
	// incomplete rollback).
	Save       func(context.Context, contract.DesktopProxyState) error
	InstanceID string
}

// RestoreResult reports what happened key by key.
type RestoreResult struct {
	// State is the ownership marker the caller must persist. On a partial
	// failure it retains only the keys that are still ours and still need
	// restore.
	State        contract.DesktopProxyState
	Restored     []string
	UserModified []string // current value is not ours; left untouched
	Failed       []string
}

type desktopTools struct {
	kind   Kind
	read   string
	write  string
	notify bool // --notify exists only in kwriteconfig6 >= 6.11
}

func (a *DesktopAdapter) resolveTools(ctx context.Context, kind Kind) (desktopTools, error) {
	lookPath := a.LookPath
	if lookPath == nil {
		lookPath = OSLookPath
	}
	if !validKind(kind) {
		return desktopTools{}, localFailure(contract.Unsupported, "platform.desktop",
			"only GNOME and KDE Plasma desktop proxy settings are supported")
	}
	if kind == KindGNOME {
		if _, err := lookPath("gsettings"); err != nil {
			return desktopTools{}, localFailure(contract.Unsupported, "platform.desktop",
				"gsettings was not found; GNOME desktop proxy settings cannot be changed")
		}
		return desktopTools{kind: KindGNOME, read: "gsettings", write: "gsettings"}, nil
	}
	for _, pair := range [][2]string{{"kwriteconfig6", "kreadconfig6"}, {"kwriteconfig5", "kreadconfig5"}} {
		if _, err := lookPath(pair[0]); err != nil {
			continue
		}
		if _, err := lookPath(pair[1]); err != nil {
			return desktopTools{}, localFailure(contract.Unsupported, "platform.desktop",
				pair[0]+" was found but "+pair[1]+" is missing; KDE desktop proxy settings cannot be changed safely")
		}
		return desktopTools{
			kind:   KindKDE,
			read:   pair[1],
			write:  pair[0],
			notify: pair[0] == "kwriteconfig6" && a.supportsNotify(ctx, pair[0]),
		}, nil
	}
	return desktopTools{}, localFailure(contract.Unsupported, "platform.desktop",
		"kwriteconfig6/5 was not found; KDE desktop proxy settings cannot be changed")
}

// supportsNotify asks the installed tool rather than assuming a version.
// --notify was added in KF 6.11; unknown options make kwriteconfig exit 1.
func (a *DesktopAdapter) supportsNotify(ctx context.Context, tool string) bool {
	out, err := a.run(ctx, tool, "--help")
	if err != nil {
		return false
	}
	return strings.Contains(out, "--notify")
}

func (a *DesktopAdapter) run(ctx context.Context, name string, args ...string) (string, error) {
	if a.Runner == nil {
		return "", errors.New("platform: desktop adapter has no command runner")
	}
	return a.Runner.Output(ctx, name, args...)
}

func (a *DesktopAdapter) readValue(ctx context.Context, tools desktopTools, layout keyLayout) (string, error) {
	switch tools.kind {
	case KindGNOME:
		out, err := a.run(ctx, tools.read, "get", layout.schema, layout.key)
		if err != nil {
			return "", err
		}
		return trimOutput(out), nil
	case KindKDE:
		out, err := a.run(ctx, tools.read,
			"--file", kdeConfigFile, "--group", kdeGroup, "--key", layout.key, "--default", kdeAbsentValue)
		if err != nil {
			return "", err
		}
		value := trimOutput(out)
		if value == kdeAbsentValue {
			return "", nil
		}
		return value, nil
	default:
		return "", errors.New("platform: unknown desktop kind")
	}
}

func (a *DesktopAdapter) writeValue(ctx context.Context, tools desktopTools, layout keyLayout, value string) error {
	if value == "" {
		return errors.New("platform: refusing to write an empty desktop setting")
	}
	switch tools.kind {
	case KindGNOME:
		// gsettings parses argv itself (it is not a GOption program), so a
		// literal "--" would be taken as the subcommand. Its values always
		// start with a quote, digit or "[", and every value passed here was
		// read back from gsettings or built by RouteLatch.
		_, err := a.run(ctx, tools.write, "set", layout.schema, layout.key, value)
		return err
	case KindKDE:
		args := []string{"--file", kdeConfigFile, "--group", kdeGroup, "--key", layout.key}
		if tools.notify {
			args = append(args, "--notify")
		}
		args = append(args, "--", value)
		_, err := a.run(ctx, tools.write, args...)
		return err
	default:
		return errors.New("platform: unknown desktop kind")
	}
}

func (a *DesktopAdapter) deleteValue(ctx context.Context, tools desktopTools, layout keyLayout) error {
	if tools.kind != KindKDE {
		return errors.New("platform: only KDE can delete a previously absent key")
	}
	args := []string{"--file", kdeConfigFile, "--group", kdeGroup, "--key", layout.key}
	if tools.notify {
		args = append(args, "--notify")
	}
	args = append(args, "--delete")
	_, err := a.run(ctx, tools.write, args...)
	return err
}

func (a *DesktopAdapter) clock() func() time.Time {
	if a.Now != nil {
		return a.Now
	}
	return time.Now
}

// Apply captures the previous value of every key before writing anything,
// writes details before switching the mode on, and rolls back every key it
// touched if any step fails. A clean rollback returns no ownership marker. If
// the rollback itself fails, Apply returns a marker for the keys that may still
// hold RouteLatch values: the caller must persist it even though the error is
// non-nil, so an offline restore can finish the job.
func (a *DesktopAdapter) Apply(ctx context.Context, opts ApplyOptions) (contract.DesktopProxyState, error) {
	if err := opts.Targets.Validate(); err != nil {
		return contract.DesktopProxyState{}, err
	}
	if opts.Previous.Applied {
		if len(opts.Previous.Keys) == 0 {
			return contract.DesktopProxyState{}, localFailure(contract.StateCorrupt, "platform.desktop.apply",
				"desktop proxy state says applied but records no settings; run `routelatch desktop proxy restore` or fix the state")
		}
		if opts.Previous.Environment != string(opts.Kind) {
			return contract.DesktopProxyState{}, localFailure(contract.Unsupported, "platform.desktop.apply",
				"a different desktop environment is already recorded as applied; run `routelatch desktop proxy restore` first")
		}
		matches, err := previousMatchesApplied(opts.Previous, opts.Kind, opts.Targets)
		if err != nil {
			return contract.DesktopProxyState{}, err
		}
		if !matches {
			return contract.DesktopProxyState{}, localFailure(contract.Unsupported, "platform.desktop.apply",
				"the desktop proxy is already applied with different listener values; run `routelatch desktop proxy restore` first")
		}
		// Re-verify both the service identity and the desktop values. Returning
		// success without either check would hide a dead/foreign listener or a
		// user edit behind a stale marker.
		if err := verifyServiceHealth(ctx, a.Health, opts.Targets, "platform.desktop.apply"); err != nil {
			return contract.DesktopProxyState{}, err
		}
		tools, err := a.resolveTools(ctx, opts.Kind)
		if err != nil {
			return contract.DesktopProxyState{}, err
		}
		for _, layout := range layoutsFor(opts.Kind) {
			var previousKey contract.DesktopKey
			for _, key := range opts.Previous.Keys {
				if key.Name == layout.name {
					previousKey = key
					break
				}
			}
			current, err := a.readValue(ctx, tools, layout)
			if err != nil {
				return contract.DesktopProxyState{}, commandFailure("platform.desktop.apply", err)
			}
			if current != previousKey.Applied {
				return contract.DesktopProxyState{}, localFailure(contract.Unsupported, "platform.desktop.apply",
					"the desktop proxy setting changed since RouteLatch applied it; run `routelatch desktop proxy restore` first")
			}
		}
		return opts.Previous, nil
	}
	if len(opts.Previous.Keys) != 0 {
		return contract.DesktopProxyState{}, localFailure(contract.StateCorrupt, "platform.desktop.apply",
			"desktop proxy state has keys but is not marked applied; nothing was changed")
	}
	layouts := layoutsFor(opts.Kind)
	if layouts == nil {
		return contract.DesktopProxyState{}, localFailure(contract.Unsupported, "platform.desktop.apply",
			"only GNOME and KDE Plasma desktop proxy settings are supported")
	}
	values, err := valuesFor(opts.Kind, opts.Targets)
	if err != nil {
		return contract.DesktopProxyState{}, err
	}
	if err := verifyServiceHealth(ctx, a.Health, opts.Targets, "platform.desktop.apply"); err != nil {
		return contract.DesktopProxyState{}, err
	}
	tools, err := a.resolveTools(ctx, opts.Kind)
	if err != nil {
		return contract.DesktopProxyState{}, err
	}

	previous := make([]string, len(layouts))
	for i, layout := range layouts {
		value, err := a.readValue(ctx, tools, layout)
		if err != nil {
			return contract.DesktopProxyState{}, commandFailure("platform.desktop.apply", err)
		}
		if value == "" && tools.kind == KindGNOME {
			return contract.DesktopProxyState{}, localFailure(contract.ExternalToolFailed, "platform.desktop.apply",
				"gsettings returned an empty value for a key that always has one; nothing was changed")
		}
		if value != "" {
			if err := validateStoredValue(value); err != nil {
				return contract.DesktopProxyState{}, localFailure(contract.Unsupported, "platform.desktop.apply",
					"the current desktop setting contains a value RouteLatch cannot safely restore; nothing was changed")
			}
		}
		previous[i] = value
	}

	marker := contract.DesktopProxyState{
		Applied:     true,
		Environment: string(opts.Kind),
		AppliedAt:   a.clock()().UTC(),
		InstanceID:  opts.InstanceID,
		Keys:        make([]contract.DesktopKey, len(layouts)),
	}
	for i, layout := range layouts {
		marker.Keys[i] = contract.DesktopKey{Name: layout.name, Applied: values[layout.name], Previous: previous[i]}
	}
	if opts.Save != nil {
		if err := opts.Save(ctx, marker); err != nil {
			return contract.DesktopProxyState{}, saveFailure(err)
		}
	}

	keys := make([]contract.DesktopKey, 0, len(layouts))
	for i, layout := range layouts {
		want := values[layout.name]
		if err := a.writeValue(ctx, tools, layout, want); err != nil {
			rbErr := a.rollback(ctx, tools, layouts, previous, i)
			if rbErr != nil {
				return a.recoveryState(opts, keys, layouts, values, previous, i), applyFailure(err, rbErr)
			}
			return contract.DesktopProxyState{}, applyFailure(err, nil)
		}
		got, err := a.readValue(ctx, tools, layout)
		if err != nil || got != want {
			rbErr := a.rollback(ctx, tools, layouts, previous, i)
			if err != nil {
				err = errors.Join(err, errors.New("value read-back failed"))
			} else {
				err = errors.New("desktop setting did not retain the applied value")
			}
			if rbErr != nil {
				return a.recoveryState(opts, keys, layouts, values, previous, i), applyFailure(err, rbErr)
			}
			return contract.DesktopProxyState{}, applyFailure(err, nil)
		}
		keys = append(keys, contract.DesktopKey{Name: layout.name, Applied: got, Previous: previous[i]})
		marker.Keys[i].Applied = got
	}

	return marker, nil
}

func saveFailure(err error) *contract.Failure {
	var failure *contract.Failure
	if errors.As(err, &failure) {
		return failure
	}
	f := localFailure(contract.Internal, "platform.desktop.apply",
		"could not record desktop proxy ownership; nothing was changed")
	f.Err = err
	return f
}

// recoveryState records every key up to and including the failed one. It is
// deliberately conservative: a restore reads each current value first, so a key
// whose rollback did succeed is skipped rather than overwritten.
func (a *DesktopAdapter) recoveryState(opts ApplyOptions, written []contract.DesktopKey, layouts []keyLayout, values map[string]string, previous []string, upto int) contract.DesktopProxyState {
	byName := make(map[string]contract.DesktopKey, len(written))
	for _, key := range written {
		byName[key.Name] = key
	}
	keys := make([]contract.DesktopKey, 0, upto+1)
	for i := 0; i <= upto; i++ {
		layout := layouts[i]
		key, ok := byName[layout.name]
		if !ok {
			key = contract.DesktopKey{Name: layout.name, Applied: values[layout.name], Previous: previous[i]}
		}
		keys = append(keys, key)
	}
	return contract.DesktopProxyState{
		Applied:     true,
		Environment: string(opts.Kind),
		AppliedAt:   a.clock()().UTC(),
		InstanceID:  opts.InstanceID,
		Keys:        keys,
	}
}

func previousMatchesApplied(previous contract.DesktopProxyState, kind Kind, targets Targets) (bool, error) {
	layouts := layoutsFor(kind)
	if layouts == nil {
		return false, localFailure(contract.Unsupported, "platform.desktop.apply",
			"only GNOME and KDE Plasma desktop proxy settings are supported")
	}
	values, err := valuesFor(kind, targets)
	if err != nil {
		return false, err
	}
	if len(previous.Keys) != len(layouts) {
		// Validate names before reporting a value mismatch so a state with an
		// unknown key is not misreported as a listener change.
		for _, key := range previous.Keys {
			if !knownLayout(layouts, key.Name) {
				return false, localFailure(contract.StateCorrupt, "platform.desktop.apply",
					"desktop proxy state names a setting this build does not manage; run `routelatch desktop proxy restore` first")
			}
		}
		return false, localFailure(contract.StateCorrupt, "platform.desktop.apply",
			"desktop proxy state is incomplete; run `routelatch desktop proxy restore` first")
	}
	byName := make(map[string]string, len(previous.Keys))
	seen := make(map[string]struct{}, len(previous.Keys))
	for _, key := range previous.Keys {
		if _, duplicate := seen[key.Name]; duplicate {
			return false, localFailure(contract.StateCorrupt, "platform.desktop.apply",
				"desktop proxy state lists the same setting twice; run `routelatch desktop proxy restore` first")
		}
		seen[key.Name] = struct{}{}
		if !knownLayout(layouts, key.Name) {
			return false, localFailure(contract.StateCorrupt, "platform.desktop.apply",
				"desktop proxy state names a setting this build does not manage; run `routelatch desktop proxy restore` first")
		}
		byName[key.Name] = key.Applied
	}
	for _, layout := range layouts {
		if byName[layout.name] != values[layout.name] {
			return false, nil
		}
	}
	return true, nil
}

func applyFailure(applyErr, rollbackErr error) *contract.Failure {
	f := commandFailure("platform.desktop.apply", applyErr)
	if rollbackErr != nil {
		f.Message = "could not apply the desktop proxy setting and rollback was incomplete; " +
			"run `routelatch desktop proxy restore` after checking the desktop settings"
		f.Err = errors.Join(applyErr, rollbackErr)
		return f
	}
	f.Message = "could not apply the desktop proxy setting; the previous values were restored and nothing is marked as applied"
	return f
}

func (a *DesktopAdapter) rollback(ctx context.Context, tools desktopTools, layouts []keyLayout, previous []string, upto int) error {
	var errs []error
	for i := upto; i >= 0; i-- {
		var err error
		switch {
		case previous[i] == "" && tools.kind == KindKDE:
			err = a.deleteValue(ctx, tools, layouts[i])
		case previous[i] == "":
			err = errors.New("cannot restore an empty GNOME value")
		default:
			err = a.writeValue(ctx, tools, layouts[i], previous[i])
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Conflicts reports which recorded settings no longer hold the value RouteLatch
// wrote, without changing anything. `desktop proxy status` uses it so an
// ownership marker the user has since edited is reported as a conflict instead
// of being presented as an unquestioned claim. A read failure is returned to the
// caller rather than reported as a conflict: an unknown value is not ownership.
func (a *DesktopAdapter) Conflicts(ctx context.Context, state contract.DesktopProxyState) ([]string, error) {
	if !state.Applied {
		return nil, nil
	}
	if len(state.Keys) == 0 {
		return nil, localFailure(contract.StateCorrupt, "platform.desktop.status",
			"desktop proxy state says applied but records no settings; cannot verify the desktop proxy")
	}
	kind := Kind(state.Environment)
	layouts := layoutsFor(kind)
	if layouts == nil {
		return nil, localFailure(contract.Unsupported, "platform.desktop.status",
			"the recorded desktop environment is not supported by this build; cannot verify the desktop proxy")
	}
	tools, err := a.resolveTools(ctx, kind)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(state.Keys))
	byName := make(map[string]contract.DesktopKey, len(state.Keys))
	for _, key := range state.Keys {
		if _, duplicate := seen[key.Name]; duplicate {
			return nil, localFailure(contract.StateCorrupt, "platform.desktop.status",
				"desktop proxy state lists the same setting twice; cannot verify the desktop proxy")
		}
		seen[key.Name] = struct{}{}
		if _, ok := layoutByName(layouts, key.Name); !ok {
			return nil, localFailure(contract.StateCorrupt, "platform.desktop.status",
				"desktop proxy state names a setting this build does not manage; cannot verify the desktop proxy")
		}
		if key.Applied == "" {
			return nil, localFailure(contract.StateCorrupt, "platform.desktop.status",
				"desktop proxy state has an empty applied value; cannot verify the desktop proxy")
		}
		byName[key.Name] = key
	}
	// Walk the layouts in reverse, the same order Restore reports user edits,
	// so the two payloads name conflicts consistently.
	var changed []string
	for i := len(layouts) - 1; i >= 0; i-- {
		key, ok := byName[layouts[i].name]
		if !ok {
			continue
		}
		current, err := a.readValue(ctx, tools, layouts[i])
		if err != nil {
			return nil, commandFailure("platform.desktop.status", err)
		}
		if current != key.Applied {
			changed = append(changed, key.Name)
		}
	}
	return changed, nil
}

// Restore works offline: it needs no running service and never consults the
// current desktop detection. It uses the environment recorded in the ownership
// marker, so a crash in a GNOME session can be repaired from a KDE session (or
// a TTY) as long as the recorded tool is installed.
func (a *DesktopAdapter) Restore(ctx context.Context, state contract.DesktopProxyState) (RestoreResult, error) {
	result := RestoreResult{State: state}
	if !state.Applied && len(state.Keys) == 0 {
		return RestoreResult{State: contract.DesktopProxyState{}}, nil
	}
	if len(state.Keys) == 0 {
		return result, stateFailure("platform.desktop.restore",
			"desktop proxy state says applied but records no settings; nothing was changed")
	}
	kind := Kind(state.Environment)
	if !validKind(kind) {
		return result, localFailure(contract.Unsupported, "platform.desktop.restore",
			"desktop proxy state names an unsupported environment; restore the desktop setting manually")
	}
	layouts := layoutsFor(kind)
	byName := make(map[string]contract.DesktopKey, len(state.Keys))
	for _, key := range state.Keys {
		if _, duplicate := byName[key.Name]; duplicate {
			return result, stateFailure("platform.desktop.restore",
				"desktop proxy state lists the same setting twice; nothing was changed")
		}
		if !knownLayout(layouts, key.Name) {
			return result, stateFailure("platform.desktop.restore",
				"desktop proxy state names a setting this build does not manage; nothing was changed")
		}
		if key.Applied == "" {
			return result, stateFailure("platform.desktop.restore",
				"desktop proxy state has an empty applied value; nothing was changed")
		}
		if kind == KindGNOME && key.Previous == "" {
			return result, stateFailure("platform.desktop.restore",
				"desktop proxy state has an empty previous value; nothing was changed")
		}
		if err := validateStoredValue(key.Previous); err != nil {
			return result, stateFailure("platform.desktop.restore",
				"desktop proxy state contains a value that cannot be passed to the desktop tool; nothing was changed")
		}
		byName[key.Name] = key
	}

	tools, err := a.resolveTools(ctx, kind)
	if err != nil {
		return result, localFailure(contract.Unsupported, "platform.desktop.restore",
			"the desktop settings tool needed to restore the proxy is not available; the desktop may still point at RouteLatch. "+
				"Install it and run `routelatch desktop proxy restore` again")
	}

	var failures []error
	for i := len(layouts) - 1; i >= 0; i-- {
		layout := layouts[i]
		key, ok := byName[layout.name]
		if !ok {
			continue
		}
		current, err := a.readValue(ctx, tools, layout)
		if err != nil {
			result.Failed = append(result.Failed, layout.name)
			failures = append(failures, err)
			continue
		}
		if current != key.Applied {
			// The user changed or deleted this key after we wrote it; it is
			// no longer ours and must not be overwritten.
			result.UserModified = append(result.UserModified, layout.name)
			continue
		}
		if key.Previous == "" {
			err = a.deleteValue(ctx, tools, layout)
		} else {
			err = a.writeValue(ctx, tools, layout, key.Previous)
		}
		if err != nil {
			result.Failed = append(result.Failed, layout.name)
			failures = append(failures, err)
			continue
		}
		got, err := a.readValue(ctx, tools, layout)
		if err != nil || got != key.Previous {
			result.Failed = append(result.Failed, layout.name)
			if err != nil {
				failures = append(failures, err)
			} else {
				failures = append(failures, errors.New("desktop setting did not retain the restored value"))
			}
			continue
		}
		result.Restored = append(result.Restored, layout.name)
	}

	if len(result.Failed) > 0 {
		failed := make(map[string]struct{}, len(result.Failed))
		for _, name := range result.Failed {
			failed[name] = struct{}{}
		}
		remaining := make([]contract.DesktopKey, 0, len(result.Failed))
		for _, layout := range layouts {
			if _, ok := failed[layout.name]; ok {
				remaining = append(remaining, byName[layout.name])
			}
		}
		result.State = contract.DesktopProxyState{
			Applied:     true,
			Environment: state.Environment,
			AppliedAt:   state.AppliedAt,
			InstanceID:  state.InstanceID,
			Keys:        remaining,
		}
		err := commandFailure("platform.desktop.restore", errors.Join(failures...))
		err.Message = "some desktop proxy settings could not be restored; they remain recorded for a later `routelatch desktop proxy restore`"
		return result, err
	}

	result.State = contract.DesktopProxyState{}
	return result, nil
}

func layoutByName(layouts []keyLayout, name string) (keyLayout, bool) {
	for _, layout := range layouts {
		if layout.name == name {
			return layout, true
		}
	}
	return keyLayout{}, false
}

func knownLayout(layouts []keyLayout, name string) bool {
	_, ok := layoutByName(layouts, name)
	return ok
}

func validateStoredValue(value string) error {
	if len(value) > 64*1024 {
		return errors.New("stored value is too long")
	}
	if !utf8.ValidString(value) {
		return errors.New("stored value is not valid UTF-8")
	}
	for _, r := range value {
		if r == 0 || r == '\n' || r == '\r' || r == 0x7f || r < 0x20 {
			return errors.New("stored value contains a control character")
		}
	}
	return nil
}

func stateFailure(op, message string) *contract.Failure {
	return localFailure(contract.StateCorrupt, op, message)
}
