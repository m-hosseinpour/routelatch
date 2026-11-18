package contract

import "context"

// SidecarExit is one hysteria2 outbound the separate sing-box executable must
// serve. It is generated from a typed Record; no imported key can reach it.
// Tag is the stable engine tag, so the engine can name one explicit exit.
type SidecarExit struct {
	Tag          string
	Server       string
	Port         uint16
	Password     string
	ServerName   string // TLS verification name (SNI); verification is never disabled
	UpMbps       uint32 // zero: RouteLatch applies its own default
	DownMbps     uint32 // zero: RouteLatch applies its own default
	Obfs         string // "" | "salamander" | "gecko"
	ObfsPassword string
}

// SidecarHandoff is the loopback endpoint RouteLatch dials to hand a request to
// one exit inside the sidecar. The credentials authenticate RouteLatch to the
// sidecar's local mixed inbound; they never leave this process.
type SidecarHandoff struct {
	Address  string // "127.0.0.1:port", always loopback
	Username string
	Password string
}

// SidecarRuntime is the narrow interface the engine uses to manage the second
// executable. It is deliberately small so a local fake can prove the handoff
// without a real sing-box binary.
//
// Invariants:
//   - Sync regenerates a fresh config and starts or restarts the process. There
//     is no add/remove-outbound RPC, so any change is full regeneration.
//     Empty exits stops the process. On any failure the runtime is failed
//     closed: no handoff is published and Healthy is false.
//   - Handoff returns the endpoint for one explicit tag; a missing tag is never
//     a cue to pick a different exit.
//   - The runtime owns one process; it makes no process-global assumptions.
type SidecarRuntime interface {
	Sync(ctx context.Context, exits []SidecarExit) error
	Handoff(tag string) (SidecarHandoff, bool)
	Healthy() bool
	Stop(ctx context.Context) error
}
