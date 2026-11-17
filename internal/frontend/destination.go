package frontend

import "routelatch/internal/contract"

// NormalizeFunc turns a client-supplied authority ("host:port") into the
// normalized destination the router and dialers consume. Production wiring
// should pass routing.NormalizeDestination, which docs/contracts.md §1.2 makes
// the only producer of normalized hostnames; the frontend uses exactly that
// function when the option is nil. The option exists so tests can observe the
// untouched authority and so a future wiring change can replace it without
// giving this package a second name-normalization path.
type NormalizeFunc func(authority string) (contract.Destination, error)
