package main

import (
	"context"
	"strings"

	"github.com/charmbracelet/huh"

	"routelatch/internal/control"
)

// menuChoice is one line of the simple menu. It only names a command; the menu
// has no routing, pool or state logic of its own (docs/contracts.md §7.3).
type menuChoice struct {
	Key   string
	Label string
}

func menuChoices() []menuChoice {
	return []menuChoice{
		{"run", "Start app (tray)"},
		{"run.headless", "Start headless (foreground)"},
		{"status", "Status"},
		{"mode", "Change mode"},
		{"rules.list", "List approved destinations"},
		{"rules.add", "Add an approved destination"},
		{"rules.remove", "Remove an approved destination"},
		{"pool.list", "Pool"},
		{"pool.add", "Add an exit (link or file)"},
		{"pool.remove", "Remove an exit"},
		{"pool.refresh", "Refresh the pool"},
		{"pool.test", "Test the pool"},
		{"pool.select", "Select an exit"},
		{"pool.rerank", "Re-rank exits now"},
		{"import", "Import a source"},
		{"diagnose", "Diagnose a destination"},
		{"proposals.list", "Proposals"},
		{"proposals.accept", "Accept a proposal"},
		{"proposals.reject", "Reject a proposal"},
		{"proposals.recheck", "Recheck a proposal"},
		{"proposals.clear", "Clear decided proposals"},
		{"config.get", "Show configuration"},
		{"config.set", "Change a configuration value"},
		{"config.path", "Show data paths"},
		{"doctor", "Health check"},
		{"env", "Show launcher environment"},
		{"exec", "Run an application through the proxy"},
		{"desktop.status", "Desktop proxy status"},
		{"desktop.set", "Apply desktop proxy"},
		{"desktop.restore", "Restore desktop proxy"},
		{"backup", "Back up state"},
		{"restore", "Restore a backup"},
		{"quit", "Quit"},
	}
}

// menuLabel sanitizes any dynamic text before it reaches a huh title or option.
// huh renders strings unchanged, so this is the terminal boundary.
func menuLabel(raw string) string {
	return control.SanitizeTerminal(raw)
}

// menuCommand maps a static menu key to the exact CLI arguments it executes.
// Interactive prompts supply the dynamic operands.
func menuCommand(key, operand, kind, note string) ([]string, bool) {
	switch key {
	case "run":
		return []string{"run"}, true
	case "run.headless":
		return []string{"run", "--headless"}, true
	case "status":
		return []string{"status"}, true
	case "mode":
		return []string{"mode", operand}, true
	case "rules.list":
		return []string{"rules", "list"}, true
	case "rules.add":
		return []string{"rules", "add", "--kind", kind, operand}, true
	case "rules.remove":
		return []string{"rules", "remove", operand}, true
	case "pool.list":
		return []string{"pool", "list"}, true
	case "pool.add":
		return []string{"pool", "add", operand}, true
	case "pool.remove":
		return []string{"pool", "remove", operand}, true
	case "pool.refresh":
		return []string{"pool", "refresh", "--all"}, true
	case "pool.test":
		return []string{"pool", "test"}, true
	case "pool.select":
		return []string{"pool", "select", operand}, true
	case "pool.rerank":
		return []string{"pool", "rerank"}, true
	case "import":
		return []string{"import", kind, operand}, true
	case "diagnose":
		return []string{"diagnose", operand}, true
	case "proposals.list":
		return []string{"proposals", "list"}, true
	case "proposals.accept":
		return []string{"proposals", "accept", operand}, true
	case "proposals.reject":
		return []string{"proposals", "reject", operand}, true
	case "proposals.recheck":
		return []string{"proposals", "recheck", operand}, true
	case "proposals.clear":
		return []string{"proposals", "clear"}, true
	case "config.get":
		return []string{"config", "get"}, true
	case "config.path":
		return []string{"config", "path"}, true
	case "config.set":
		return []string{"config", "set", operand, kind}, true
	case "doctor":
		return []string{"doctor"}, true
	case "env":
		return []string{"env"}, true
	case "exec":
		return []string{"exec", "--", operand}, true
	case "desktop.status":
		return []string{"desktop", "proxy", "status"}, true
	case "desktop.set":
		return []string{"desktop", "proxy", "set"}, true
	case "desktop.restore":
		return []string{"desktop", "proxy", "restore"}, true
	case "backup":
		return []string{"backup"}, true
	case "restore":
		return []string{"restore", operand}, true
	case "quit":
		return nil, false
	default:
		return nil, false
	}
}

func (c *cli) menu(ctx context.Context) int {
	choices := menuChoices()
	options := make([]huh.Option[string], 0, len(choices))
	for _, choice := range choices {
		options = append(options, huh.NewOption(menuLabel(choice.Label), choice.Key))
	}
	for {
		var selected string
		err := huh.NewForm(huh.NewGroup(
			huh.NewSelect[string]().Title("RouteLatch").Options(options...).Value(&selected),
		)).RunWithContext(ctx)
		if err != nil {
			return 0
		}
		if selected == "quit" {
			return 0
		}
		args, ok := c.menuArgs(ctx, selected)
		if !ok {
			continue
		}
		_ = c.dispatch(ctx, args)
	}
}

// menuArgs asks for the operands one command needs, then returns exactly the
// CLI arguments. Destructive actions require an explicit yes (default no).
func (c *cli) menuArgs(ctx context.Context, key string) ([]string, bool) {
	switch key {
	case "mode":
		var mode string
		if err := c.selectPrompt(ctx, "Routing mode", []huh.Option[string]{
			huh.NewOption("Targeted (approved destinations use the pool)", "targeted"),
			huh.NewOption("All proxy (local/LAN direct)", "all-proxy"),
			huh.NewOption("All direct (no pool)", "all-direct"),
			huh.NewOption("Stopped (closes listeners; restart needed)", "stopped"),
		}, &mode); err != nil {
			return nil, false
		}
		if mode == "stopped" && !c.confirm(ctx, "Stop listening? Every connection is closed and leaving stopped mode needs a restart.") {
			return nil, false
		}
		return menuCommand(key, mode, "", "")
	case "rules.add":
		var host string
		if err := c.input(ctx, "Hostname, IP or CIDR", &host); err != nil || strings.TrimSpace(host) == "" {
			return nil, false
		}
		var kind string
		if err := c.selectPrompt(ctx, "Rule kind", []huh.Option[string]{
			huh.NewOption("Exact hostname", "domain"),
			huh.NewOption("Hostname and subdomains", "domain_suffix"),
			huh.NewOption("IP address", "ip"),
			huh.NewOption("CIDR range", "cidr"),
		}, &kind); err != nil {
			return nil, false
		}
		return menuCommand(key, host, kind, "")
	case "rules.remove":
		var id string
		if err := c.input(ctx, "Rule id", &id); err != nil || id == "" {
			return nil, false
		}
		if !c.confirm(ctx, "Remove rule "+menuLabel(id)+"? Direct connections to it close.") {
			return nil, false
		}
		return menuCommand(key, id, "", "")
	case "pool.add":
		var input string
		if err := c.input(ctx, "Share link or file path", &input); err != nil || input == "" {
			return nil, false
		}
		return menuCommand(key, input, "", "")
	case "pool.remove":
		var id string
		if err := c.input(ctx, "Record id", &id); err != nil || id == "" {
			return nil, false
		}
		if !c.confirm(ctx, "Remove exit "+menuLabel(id)+"? Its live connections close.") {
			return nil, false
		}
		return menuCommand(key, id, "", "")
	case "pool.select":
		var id string
		if err := c.input(ctx, "Exit id or 'auto'", &id); err != nil || id == "" {
			return nil, false
		}
		return menuCommand(key, id, "", "")
	case "import":
		var kind string
		if err := c.selectPrompt(ctx, "Import kind", []huh.Option[string]{
			huh.NewOption("One share link", "link"),
			huh.NewOption("Local file", "file"),
			huh.NewOption("Subscription URL", "subscription"),
			huh.NewOption("v2rayN export", "v2rayn"),
		}, &kind); err != nil {
			return nil, false
		}
		var input string
		if err := c.input(ctx, "Link, file path or URL", &input); err != nil || input == "" {
			return nil, false
		}
		return menuCommand(key, input, kind, "")
	case "diagnose":
		var input string
		if err := c.input(ctx, "Destination host:port", &input); err != nil || input == "" {
			return nil, false
		}
		return menuCommand(key, input, "", "")
	case "proposals.accept", "proposals.reject", "proposals.recheck":
		var id string
		if err := c.input(ctx, "Proposal id", &id); err != nil || id == "" {
			return nil, false
		}
		return menuCommand(key, id, "", "")
	case "proposals.clear":
		if !c.confirm(ctx, "Clear decided and expired proposals?") {
			return nil, false
		}
		return menuCommand(key, "", "", "")
	case "config.set":
		var keyName, value string
		if err := c.input(ctx, "Config key", &keyName); err != nil || keyName == "" {
			return nil, false
		}
		if err := c.input(ctx, "Value", &value); err != nil {
			return nil, false
		}
		return menuCommand(key, keyName, value, "")
	case "desktop.set":
		if !c.confirm(ctx, "Point this desktop's proxy at RouteLatch?") {
			return nil, false
		}
	case "desktop.restore":
		if !c.confirm(ctx, "Restore the previous desktop proxy values?") {
			return nil, false
		}
	case "exec":
		var commandLine string
		if err := c.input(ctx, "Command and arguments", &commandLine); err != nil || strings.TrimSpace(commandLine) == "" {
			return nil, false
		}
		fields := strings.Fields(commandLine)
		if len(fields) == 0 {
			return nil, false
		}
		return append([]string{"exec", "--"}, fields...), true
	case "restore":
		var path string
		if err := c.input(ctx, "Backup path", &path); err != nil || path == "" {
			return nil, false
		}
		if !c.confirm(ctx, "Restore state from "+menuLabel(path)+"? Current state is backed up first.") {
			return nil, false
		}
		return menuCommand(key, path, "", "")
	}
	return menuCommand(key, "", "", "")
}

func (c *cli) input(ctx context.Context, title string, value *string) error {
	return huh.NewForm(huh.NewGroup(
		huh.NewInput().Title(menuLabel(title)).Value(value),
	)).RunWithContext(ctx)
}

func (c *cli) selectPrompt(ctx context.Context, title string, options []huh.Option[string], value *string) error {
	return huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().Title(menuLabel(title)).Options(options...).Value(value),
	)).RunWithContext(ctx)
}

func (c *cli) confirm(ctx context.Context, title string) bool {
	yes := false
	err := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().Title(menuLabel(title)).Affirmative("Yes").Negative("No").Value(&yes),
	)).RunWithContext(ctx)
	return err == nil && yes
}
