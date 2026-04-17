package rawproxy

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/net/proxy"
)

// UpstreamDialer wraps a net.Dialer with optional SOCKS5 proxy support.
// For HTTP upstream proxies, use UpstreamHTTPProxyFunc instead.
type UpstreamDialer struct {
	base    *net.Dialer
	socksOf proxy.Dialer // non-nil if SOCKS5
}

// NewUpstreamDialer creates a dialer that optionally routes through an upstream proxy.
// Supports:
//   - "" (empty) — direct connection
//   - "socks5://host:port" — SOCKS5 proxy
//   - SOCKS5 auth: "socks5://user:pass@host:port"
func NewUpstreamDialer(upstreamProxy string) (*UpstreamDialer, error) {
	base := &net.Dialer{
		Timeout:   15 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	ud := &UpstreamDialer{base: base}

	if upstreamProxy == "" {
		return ud, nil
	}

	u, err := url.Parse(upstreamProxy)
	if err != nil {
		return nil, fmt.Errorf("invalid upstream proxy URL: %w", err)
	}

	switch u.Scheme {
	case "socks5", "socks5h":
		var auth *proxy.Auth
		if u.User != nil {
			pass, _ := u.User.Password()
			auth = &proxy.Auth{
				User:     u.User.Username(),
				Password: pass,
			}
		}
		socksDialer, err := proxy.SOCKS5("tcp", u.Host, auth, base)
		if err != nil {
			return nil, fmt.Errorf("failed to create SOCKS5 dialer: %w", err)
		}
		ud.socksOf = socksDialer
		log.Printf("[Proxy] Using SOCKS5 upstream: %s", u.Host)

	case "http", "https":
		// HTTP upstream is handled at the Transport level, not dialer level.
		// This dialer remains direct; the Transport.Proxy function handles it.
		log.Printf("[Proxy] Using HTTP upstream: %s", u.String())

	default:
		return nil, fmt.Errorf("unsupported upstream proxy scheme: %s (use http, https, or socks5)", u.Scheme)
	}

	return ud, nil
}

// DialContext connects through the upstream proxy if configured.
func (ud *UpstreamDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if ud.socksOf != nil {
		return ud.socksOf.(proxy.ContextDialer).DialContext(ctx, network, addr)
	}
	return ud.base.DialContext(ctx, network, addr)
}

// UpstreamHTTPProxyFunc returns an http.Transport Proxy function for HTTP upstream proxies.
// Returns nil if the upstream is not an HTTP proxy (e.g., SOCKS5 or empty).
func UpstreamHTTPProxyFunc(upstreamProxy string) func(*http.Request) (*url.URL, error) {
	if upstreamProxy == "" {
		return nil
	}
	u, err := url.Parse(upstreamProxy)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil
	}
	return http.ProxyURL(u)
}

// LoadClientCert loads a PEM-encoded client certificate and key for mTLS.
// Returns nil if both paths are empty.
func LoadClientCert(certFile, keyFile string) (*tls.Certificate, error) {
	if certFile == "" && keyFile == "" {
		return nil, nil
	}
	if certFile == "" || keyFile == "" {
		return nil, fmt.Errorf("both client_cert and client_key must be specified for mTLS")
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load client certificate: %w", err)
	}
	log.Printf("[Proxy] Loaded mTLS client certificate from %s", certFile)
	return &cert, nil
}
