//go:build tools

package contract

// This file only pins dependencies that later tasks will import. It is excluded
// from normal builds (build tag "tools") and exists so `go mod tidy` keeps the
// three approved modules in go.mod and go.sum before their importers land. Do
// not import it, and do not add a fourth module here without a contract change
// (docs/contracts.md §0).
import (
	_ "github.com/charmbracelet/huh"
	_ "github.com/things-go/go-socks5"
	_ "github.com/xtls/xray-core/main/distro/all"
	_ "golang.org/x/net/publicsuffix"
)
