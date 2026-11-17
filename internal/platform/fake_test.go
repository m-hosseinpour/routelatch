package platform

import (
	"context"
	"errors"
)

type fakeCall struct {
	name string
	args []string
}

// fakeSystem is a small model of gsettings/kreadconfig/kwriteconfig. It keeps
// values in memory, records every argv slice, and never touches a real desktop
// or file.
type fakeSystem struct {
	values         map[string]string // GNOME "schema key" or KDE key
	calls          []fakeCall
	look           map[string]bool
	notifyHelp     map[string]bool
	writeErr       map[string]error // fails once, keyed like values
	alwaysWriteErr map[string]error // fails every time
	readErr        map[string]error
}

func newFakeSystem(values map[string]string) *fakeSystem {
	f := &fakeSystem{
		values:         make(map[string]string, len(values)),
		look:           map[string]bool{},
		notifyHelp:     map[string]bool{},
		writeErr:       map[string]error{},
		alwaysWriteErr: map[string]error{},
		readErr:        map[string]error{},
	}
	for k, v := range values {
		f.values[k] = v
	}
	return f
}

func (f *fakeSystem) LookPath(name string) (string, error) {
	if f.look[name] {
		return "/fake/bin/" + name, nil
	}
	return "", errors.New("executable file not found in $PATH")
}

func (f *fakeSystem) Output(_ context.Context, name string, args ...string) (string, error) {
	f.calls = append(f.calls, fakeCall{name: name, args: append([]string(nil), args...)})
	switch name {
	case "gsettings":
		return f.gsettings(args)
	case "kreadconfig6", "kreadconfig5":
		return f.kreadconfig(args)
	case "kwriteconfig6", "kwriteconfig5":
		if len(args) == 1 && args[0] == "--help" {
			if f.notifyHelp[name] {
				return "Options:\n  --notify  Notify applications of the change\n", nil
			}
			return "Options:\n", nil
		}
		return f.kwriteconfig(args)
	default:
		return "", errors.New("unexpected command: " + name)
	}
}

func (f *fakeSystem) gsettings(args []string) (string, error) {
	if len(args) < 3 {
		return "", errors.New("short gsettings command")
	}
	key := args[1] + " " + args[2]
	switch args[0] {
	case "get":
		if err := f.readErr[key]; err != nil {
			delete(f.readErr, key)
			return "", err
		}
		value, ok := f.values[key]
		if !ok {
			return "", errors.New("schema key not found")
		}
		return value + "\n", nil
	case "set":
		if len(args) != 4 {
			return "", errors.New("gsettings set needs schema key value")
		}
		if err := f.alwaysWriteErr[key]; err != nil {
			return "", err
		}
		if err := f.writeErr[key]; err != nil {
			delete(f.writeErr, key)
			return "", err
		}
		f.values[key] = args[3]
		return "", nil
	default:
		return "", errors.New("unexpected gsettings subcommand")
	}
}

func (f *fakeSystem) kreadconfig(args []string) (string, error) {
	key, dflt, err := parseKDEArgs(args)
	if err != nil {
		return "", err
	}
	if err := f.readErr[key]; err != nil {
		delete(f.readErr, key)
		return "", err
	}
	if value, ok := f.values[key]; ok {
		return value + "\n", nil
	}
	return dflt + "\n", nil
}

func (f *fakeSystem) kwriteconfig(args []string) (string, error) {
	key, _, err := parseKDEArgs(args)
	if err != nil {
		return "", err
	}
	deleteKey := false
	value := ""
	sawValue := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--delete":
			deleteKey = true
		case "--notify", "--file", "--group", "--key":
			if args[i] != "--notify" {
				i++
			}
		case "--":
			if i+1 < len(args) {
				value = args[i+1]
				sawValue = true
				i++
			}
		default:
			value = args[i]
			sawValue = true
		}
	}
	if err := f.alwaysWriteErr[key]; err != nil {
		return "", err
	}
	if err := f.writeErr[key]; err != nil {
		delete(f.writeErr, key)
		return "", err
	}
	if deleteKey {
		delete(f.values, key)
		return "", nil
	}
	if !sawValue {
		return "", errors.New("kwriteconfig has no value")
	}
	f.values[key] = value
	return "", nil
}

func parseKDEArgs(args []string) (key, dflt string, err error) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--key", "--default":
			if i+1 >= len(args) {
				return "", "", errors.New("missing option value")
			}
			if args[i] == "--key" {
				key = args[i+1]
			} else {
				dflt = args[i+1]
			}
			i++
		}
	}
	if key == "" {
		return "", "", errors.New("missing --key")
	}
	return key, dflt, nil
}

func (f *fakeSystem) clearCalls() { f.calls = nil }
