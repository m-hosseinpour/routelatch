package main

import (
	"context"
	"errors"
	"strings"

	"routelatch/internal/control"
)

func (c *cli) mode(ctx context.Context, args []string) int {
	if len(args) != 1 {
		return c.localFailure("mode", usageErr("mode needs one of targeted, all-proxy, all-direct, stopped"))
	}
	return c.call(ctx, control.Request{Op: control.OpMode, Mode: args[0]})
}

func (c *cli) rules(ctx context.Context, args []string) int {
	if len(args) == 0 {
		return c.localFailure("rules", usageErr("rules needs list, add or remove"))
	}
	switch args[0] {
	case "list":
		return c.call(ctx, control.Request{Op: control.OpRulesList})
	case "add":
		fs := c.newFlags("rules add")
		kind := fs.String("kind", "", "domain|domain_suffix|ip|cidr")
		note := fs.String("note", "", "display note")
		if err := fs.Parse(args[1:]); err != nil {
			return c.localFailure("rules add", usageErr("invalid rules add flags"))
		}
		if fs.NArg() != 1 {
			return c.localFailure("rules add", usageErr("rules add needs one hostname, IP or CIDR"))
		}
		return c.call(ctx, control.Request{Op: control.OpRulesAdd, Input: fs.Arg(0), RuleKind: *kind, Note: *note})
	case "remove":
		if len(args) != 2 {
			return c.localFailure("rules remove", usageErr("rules remove needs one rule id"))
		}
		return c.call(ctx, control.Request{Op: control.OpRulesRemove, ID: args[1]})
	default:
		return c.localFailure("rules", usageErr("unknown rules subcommand"))
	}
}

func (c *cli) pool(ctx context.Context, args []string) int {
	if len(args) == 0 {
		return c.localFailure("pool", usageErr("pool needs list, add, remove, refresh, test or select"))
	}
	switch args[0] {
	case "list":
		fs := c.newFlags("pool list")
		limit := fs.Int("limit", 100, "page size")
		offset := fs.Int("offset", 0, "page offset")
		if err := fs.Parse(args[1:]); err != nil {
			return c.localFailure("pool list", usageErr("invalid pool list flags"))
		}
		return c.call(ctx, control.Request{Op: control.OpPoolList, Limit: *limit, Offset: *offset})
	case "add":
		fs := c.newFlags("pool add")
		file := fs.Bool("file", false, "treat the input as a local file")
		if err := fs.Parse(args[1:]); err != nil {
			return c.localFailure("pool add", usageErr("invalid pool add flags"))
		}
		if fs.NArg() != 1 {
			return c.localFailure("pool add", usageErr("pool add needs one share link or file path"))
		}
		kind := ""
		if *file {
			kind = "file"
		}
		return c.call(ctx, control.Request{Op: control.OpPoolAdd, Input: fs.Arg(0), Kind: kind})
	case "remove":
		if len(args) != 2 {
			return c.localFailure("pool remove", usageErr("pool remove needs one record id"))
		}
		return c.call(ctx, control.Request{Op: control.OpPoolRemove, ID: args[1]})
	case "refresh":
		fs := c.newFlags("pool refresh")
		source := fs.String("source", "", "source id")
		all := fs.Bool("all", false, "refresh every source")
		allowEmpty := fs.Bool("allow-empty", false, "accept a source with zero records")
		if err := fs.Parse(args[1:]); err != nil {
			return c.localFailure("pool refresh", usageErr("invalid pool refresh flags"))
		}
		if fs.NArg() > 0 {
			return c.localFailure("pool refresh", usageErr("pool refresh takes no positional arguments"))
		}
		return c.call(ctx, control.Request{Op: control.OpPoolRefresh, SourceID: *source, All: *all, AllowEmpty: *allowEmpty})
	case "test":
		fs := c.newFlags("pool test")
		exit := fs.String("exit", "", "one exit id")
		target := fs.String("target", "", "egress target host:port")
		if err := fs.Parse(args[1:]); err != nil {
			return c.localFailure("pool test", usageErr("invalid pool test flags"))
		}
		if fs.NArg() > 1 {
			return c.localFailure("pool test", usageErr("pool test takes at most one target"))
		}
		if fs.NArg() == 1 {
			*target = fs.Arg(0)
		}
		return c.call(ctx, control.Request{Op: control.OpPoolTest, ID: *exit, Input: *target})
	case "select":
		if len(args) != 2 {
			return c.localFailure("pool select", usageErr("pool select needs one exit id or 'auto'"))
		}
		return c.call(ctx, control.Request{Op: control.OpPoolSelect, ID: args[1]})
	case "rerank":
		if len(args) != 1 {
			return c.localFailure("pool rerank", usageErr("pool rerank takes no arguments"))
		}
		return c.call(ctx, control.Request{Op: control.OpPoolRerank})
	default:
		return c.localFailure("pool", usageErr("unknown pool subcommand"))
	}
}

func (c *cli) importCmd(ctx context.Context, args []string) int {
	if len(args) != 2 {
		return c.localFailure("import", usageErr("import needs a kind (link, file, subscription, v2rayn) and an input"))
	}
	return c.call(ctx, control.Request{Op: control.OpImport, Kind: args[0], Input: args[1]})
}

func (c *cli) diagnose(ctx context.Context, args []string) int {
	fs := c.newFlags("diagnose")
	direct := fs.Bool("direct", false, "direct probes only")
	poolFlag := fs.Bool("pool", false, "pool-path diagnosis (outside v1)")
	if err := fs.Parse(args); err != nil {
		return c.localFailure("diagnose", usageErr("invalid diagnose flags"))
	}
	if fs.NArg() != 1 {
		return c.localFailure("diagnose", usageErr("diagnose needs one host:port"))
	}
	kind := ""
	if *poolFlag && !*direct {
		kind = "pool"
	}
	return c.call(ctx, control.Request{Op: control.OpDiagnose, Input: fs.Arg(0), Kind: kind})
}

func (c *cli) proposals(ctx context.Context, args []string) int {
	if len(args) == 0 {
		return c.localFailure("proposals", usageErr("proposals needs list, accept, reject, recheck or clear"))
	}
	switch args[0] {
	case "list":
		return c.call(ctx, control.Request{Op: control.OpProposalsList})
	case "accept":
		if len(args) != 2 {
			return c.localFailure("proposals accept", usageErr("proposals accept needs one proposal id"))
		}
		return c.call(ctx, control.Request{Op: control.OpProposalsAccept, ID: args[1]})
	case "reject":
		fs := c.newFlags("proposals reject")
		note := fs.String("note", "", "decision note")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 1 {
			return c.localFailure("proposals reject", usageErr("proposals reject needs one proposal id"))
		}
		return c.call(ctx, control.Request{Op: control.OpProposalsReject, ID: fs.Arg(0), Note: *note})
	case "recheck":
		if len(args) != 2 {
			return c.localFailure("proposals recheck", usageErr("proposals recheck needs one proposal id"))
		}
		return c.call(ctx, control.Request{Op: control.OpProposalsRecheck, ID: args[1]})
	case "clear":
		return c.call(ctx, control.Request{Op: control.OpProposalsClear})
	default:
		return c.localFailure("proposals", usageErr("unknown proposals subcommand"))
	}
}

func (c *cli) config(ctx context.Context, args []string) int {
	if len(args) == 0 {
		return c.localFailure("config", usageErr("config needs get, set or path"))
	}
	switch args[0] {
	case "get":
		return c.call(ctx, control.Request{Op: control.OpConfigGet})
	case "path":
		return c.call(ctx, control.Request{Op: control.OpConfigPath})
	case "set":
		if len(args) < 3 {
			return c.localFailure("config set", usageErr("config set needs a key and a value"))
		}
		value := strings.Join(args[2:], " ")
		return c.call(ctx, control.Request{Op: control.OpConfigSet, Key: args[1], Value: value})
	default:
		return c.localFailure("config", usageErr("unknown config subcommand"))
	}
}

func (c *cli) desktop(ctx context.Context, args []string) int {
	if len(args) < 2 || args[0] != "proxy" {
		return c.localFailure("desktop proxy", usageErr("desktop needs 'proxy status|set|restore'"))
	}
	switch args[1] {
	case "status":
		return c.call(ctx, control.Request{Op: control.OpDesktopStatus})
	case "set":
		return c.call(ctx, control.Request{Op: control.OpDesktopSet})
	case "restore":
		env, err := c.client().Call(ctx, control.Request{Op: control.OpDesktopRestore})
		if err == nil {
			return c.print(env)
		}
		if errors.Is(err, control.ErrNotRunning) {
			if code, handled := c.desktopRestoreOffline(ctx); handled {
				return code
			}
		}
		return c.localFailure("desktop proxy restore", err)
	default:
		return c.localFailure("desktop proxy", usageErr("unknown desktop proxy subcommand"))
	}
}
