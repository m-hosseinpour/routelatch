package imports

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"routelatch/internal/contract"
)

var _ contract.Fetcher = (*HTTPFetcher)(nil)

// HTTPFetcher implements contract.Fetcher. It never uses an environment proxy,
// never follows a redirect to another scheme, never sends a stored credential
// to a changed host, and never reads more than MaxSourceBytes.
type HTTPFetcher struct {
	client *http.Client
}

// NewFetcher builds the v1 fetcher with the contract limits.
func NewFetcher() *HTTPFetcher {
	transport := &http.Transport{
		Proxy: nil, // never inherit HTTP(S)_PROXY/ALL_PROXY (contract §5.6)
		DialContext: (&net.Dialer{
			Timeout:   contract.SourceConnectTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   contract.SourceConnectTimeout,
		ResponseHeaderTimeout: contract.SourceTotalTimeout,
		DisableKeepAlives:     true,
		ForceAttemptHTTP2:     true,
	}
	return &HTTPFetcher{client: &http.Client{
		Transport: transport,
		Timeout:   contract.SourceTotalTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > contract.SourceMaxRedirects {
				return errTooManyRedirects
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return errRedirectScheme
			}
			// net/http already drops Authorization on a cross-host redirect,
			// but cookies and any future sensitive header need the explicit
			// rule the contract states.
			if len(via) > 0 && !sameHost(via[len(via)-1].URL, req.URL) {
				req.Header.Del("Authorization")
				req.Header.Del("Proxy-Authorization")
				req.Header.Del("Cookie")
			}
			return nil
		},
	}}
}

var (
	errTooManyRedirects = errors.New("redirect limit reached")
	errRedirectScheme   = errors.New("redirect changed scheme")
	errOversized        = errors.New("response body exceeds the source limit")
	errHTML             = errors.New("response is HTML")
)

func sameHost(a, b *url.URL) bool {
	return strings.EqualFold(a.Hostname(), b.Hostname())
}

// FetchSource implements contract.Fetcher. Only subscription sources are
// fetched; file/v2rayN/manual sources are read by the caller and never by a
// network client.
func (f *HTTPFetcher) FetchSource(ctx context.Context, src contract.Source) ([]byte, error) {
	if src.Kind != contract.SourceSubscription {
		return nil, sourceFailure("source kind does not use the network", nil)
	}
	u, err := url.Parse(strings.TrimSpace(src.Origin))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, sourceFailure("subscription URL must be http or https", nil)
	}
	ctx, cancel := context.WithTimeout(ctx, contract.SourceTotalTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, sourceFailure("subscription request could not be built", err)
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", "RouteLatch/1")

	resp, err := f.client.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, canceledFailure("imports.fetch")
		}
		if ctx.Err() != nil {
			return nil, &contract.Failure{
				Kind: contract.Timeout, Path: contract.PathLocal, Op: "imports.fetch",
				Message: "subscription request timed out", Err: err,
			}
		}
		if errors.Is(err, errTooManyRedirects) {
			return nil, sourceFailure("subscription redirected too many times", err)
		}
		if errors.Is(err, errRedirectScheme) {
			return nil, sourceFailure("subscription redirected to an unsupported scheme", err)
		}
		return nil, sourceFailure("subscription fetch failed", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, sourceFailure(fmt.Sprintf("subscription returned HTTP %d", resp.StatusCode), nil)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, contract.MaxSourceBytes+1))
	if err != nil {
		return nil, sourceFailure("subscription body could not be read", err)
	}
	if len(body) > contract.MaxSourceBytes {
		return nil, sourceFailure("subscription body exceeds the 4 MiB limit", errOversized)
	}
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		if src.AllowEmpty {
			return body, nil
		}
		return nil, sourceFailure("subscription is empty", nil)
	}
	if strings.HasPrefix(trimmed, "<") {
		return nil, sourceFailure("subscription returned HTML, not a subscription", errHTML)
	}
	return body, nil
}
