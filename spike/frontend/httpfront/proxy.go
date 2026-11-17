// Package httpfront contains the HTTP/CONNECT frontend experiment for spike/T1.
//
// It is evidence, not production code. New() is the configuration we would have
// to impose on github.com/elazarl/goproxy v1.9.4 to approach the RouteLatch
// frontend guarantees; the surrounding tests measure which guarantees still
// fail after that configuration.
package httpfront

import (
	"context"
	"net"
	"net/http"

	"github.com/elazarl/goproxy"
)

// Config is the part of the frontend our routing layer would own.
type Config struct {
	// Dial receives the destination address exactly as the client supplied it.
	// It must not be given a locally resolved IP by the frontend.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// DisableKeepAlives removes the upstream http.Transport pool. The pool is
	// what enables transparent replay of idempotent requests (see tests).
	DisableKeepAlives bool
}

// New returns a goproxy configuration with every optional behaviour that could
// change routing or bytes turned off or redirected through Dial.
func New(cfg Config) *goproxy.ProxyHttpServer {
	p := goproxy.NewProxyHttpServer()
	p.Verbose = false

	// The stock transport reads HTTP_PROXY/HTTPS_PROXY from the environment and
	// owns its own dialer. Neither is allowed: routing policy is ours.
	p.Tr = &http.Transport{
		Proxy:              nil,
		DialContext:        cfg.Dial,
		DisableCompression: true,
		DisableKeepAlives:  cfg.DisableKeepAlives,
		ForceAttemptHTTP2:  false,
	}
	// Do not delete the client's Accept-Encoding, which would make the transport
	// add gzip and transparently decompress the body.
	p.KeepAcceptEncoding = true

	// CONNECT must go through our dialer, with the hostname as received.
	p.ConnectDialWithReq = func(req *http.Request, network, addr string) (net.Conn, error) {
		return cfg.Dial(req.Context(), network, addr)
	}

	// An absolute-form https:// request would make this proxy terminate TLS to
	// the origin and act as a fetcher. A forward proxy for v1 must refuse it.
	p.OnRequest(goproxy.ReqConditionFunc(func(r *http.Request, _ *goproxy.ProxyCtx) bool {
		return r.URL.Scheme == "https"
	})).DoFunc(func(r *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
		return r, goproxy.NewResponse(r, goproxy.ContentTypeText, http.StatusForbidden,
			"absolute-form https requests are not supported")
	})

	return p
}
