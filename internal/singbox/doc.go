// Package singbox manages a separate sing-box v1.14.4 executable as a RouteLatch
// sidecar. It is an arm's-length process: RouteLatch writes a fresh JSON config,
// starts the binary, and hands requests to a loopback authenticated inbound.
// No sing-box code is linked into this binary, and no imported value is ever
// executed, fetched, or used as a path.
//
// The generated config deliberately has no dns section and no resolve rules:
// only the hysteria2 exit's own server address may be resolved locally, and the
// destination hostname is preserved across the tunnel (D15). Logging is
// disabled so no destination reaches a log (D21). Verification is always on.
// There is no add/remove-outbound RPC, so every change is a full regeneration
// and process restart; any failure leaves the runtime failed closed.
package singbox
