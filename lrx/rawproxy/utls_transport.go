package rawproxy

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
)

// BrowserFingerprint represents different browser TLS fingerprints to mimic
type BrowserFingerprint int

const (
	FingerprintChrome BrowserFingerprint = iota
	FingerprintFirefox
	FingerprintSafari
	FingerprintEdge
	FingerprintRandom // Randomly pick one
)

// hostProtoCache is a global cache of host → negotiated protocol.
// Once we learn a host speaks "http/1.1" (or "h2"), all future
// UTLSRoundTripper instances for that host reuse the result.
var hostProtoCache sync.Map // map[string]string  (host:port → "h2" | "http/1.1")

// UTLSRoundTripper is an http.RoundTripper that uses uTLS for TLS connections
// and properly handles HTTP/2 based on ALPN negotiation
type UTLSRoundTripper struct {
	fingerprint    BrowserFingerprint
	serverName     string
	dialer         ContextDialer // supports SOCKS5 upstream
	clientCert     *tls.Certificate // mTLS client certificate (optional)
	http2Transport *http2.Transport
	http1Transport *http.Transport
}

// ContextDialer is the interface for dialers that support context.
type ContextDialer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

// NewUTLSRoundTripper creates a new round tripper with browser fingerprint spoofing
func NewUTLSRoundTripper(serverName string, fingerprint BrowserFingerprint) *UTLSRoundTripper {
	return NewUTLSRoundTripperWithOptions(serverName, fingerprint, nil, nil)
}

// NewUTLSRoundTripperWithOptions creates a round tripper with optional upstream dialer and client cert.
func NewUTLSRoundTripperWithOptions(serverName string, fingerprint BrowserFingerprint, customDialer ContextDialer, clientCert *tls.Certificate) *UTLSRoundTripper {
	var dialer ContextDialer
	if customDialer != nil {
		dialer = customDialer
	} else {
		dialer = &net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}
	}
	rt := &UTLSRoundTripper{
		fingerprint: fingerprint,
		serverName:  serverName,
		dialer:      dialer,
		clientCert:  clientCert,
	}

	// HTTP/1.1 transport with custom TLS dialer
	rt.http1Transport = &http.Transport{
		DialTLSContext:        rt.dialTLSForHTTP1,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ForceAttemptHTTP2:     false, // We handle HTTP/2 separately
	}

	// HTTP/2 transport with custom TLS dialer
	rt.http2Transport = &http2.Transport{
		DialTLSContext:  rt.dialTLSForHTTP2,
		ReadIdleTimeout: 30 * time.Second,
		PingTimeout:     15 * time.Second,
	}

	return rt
}

// getClientHelloID returns the uTLS ClientHelloID for the specified fingerprint
func (rt *UTLSRoundTripper) getClientHelloID() utls.ClientHelloID {
	switch rt.fingerprint {
	case FingerprintChrome:
		return utls.HelloChrome_Auto
	case FingerprintFirefox:
		return utls.HelloFirefox_Auto
	case FingerprintSafari:
		return utls.HelloSafari_Auto
	case FingerprintEdge:
		return utls.HelloEdge_Auto
	case FingerprintRandom:
		return utls.HelloRandomized
	default:
		return utls.HelloChrome_Auto
	}
}

// dialUTLS creates a uTLS connection with the specified ALPN protocols
func (rt *UTLSRoundTripper) dialUTLS(ctx context.Context, network, addr string, alpnProtos []string) (*utls.UConn, error) {
	// Extract hostname for SNI
	serverName := rt.serverName
	if serverName == "" {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			serverName = addr
		} else {
			serverName = host
		}
	}

	// Dial TCP connection
	tcpConn, err := rt.dialer.DialContext(ctx, network, addr)
	if err != nil {
		return nil, fmt.Errorf("failed to dial %s: %w", addr, err)
	}

	// Create uTLS config with specified ALPN
	config := &utls.Config{
		ServerName:         serverName,
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS12,
		MaxVersion:         tls.VersionTLS13,
		NextProtos:         alpnProtos,
	}
	// mTLS: inject client certificate if configured
	if rt.clientCert != nil {
		utlsCert := utls.Certificate{
			Certificate:                 rt.clientCert.Certificate,
			PrivateKey:                  rt.clientCert.PrivateKey,
			OCSPStaple:                  rt.clientCert.OCSPStaple,
			SignedCertificateTimestamps: rt.clientCert.SignedCertificateTimestamps,
			Leaf:                        rt.clientCert.Leaf,
		}
		config.Certificates = []utls.Certificate{utlsCert}
	}

	// Create uTLS connection with browser fingerprint
	utlsConn := utls.UClient(tcpConn, config, rt.getClientHelloID())

	// Perform TLS handshake
	if err := utlsConn.HandshakeContext(ctx); err != nil {
		tcpConn.Close()
		return nil, fmt.Errorf("uTLS handshake failed for %s: %w", serverName, err)
	}

	// Cache JA4 fingerprint from the negotiated TLS parameters.
	// utls.ConnectionState is its own type; convert relevant fields.
	connState := utlsConn.ConnectionState()
	fp := ComputeJA4(serverName, TLSStateSnapshot{
		Version:            connState.Version,
		CipherSuite:        connState.CipherSuite,
		NegotiatedProtocol: connState.NegotiatedProtocol,
		ServerName:         connState.ServerName,
		PeerCertificates:   connState.PeerCertificates,
	})
	CacheJA4(serverName, fp)

	return utlsConn, nil
}

// dialTLSForHTTP1 creates a TLS connection for HTTP/1.1 (only requests http/1.1 via ALPN)
func (rt *UTLSRoundTripper) dialTLSForHTTP1(ctx context.Context, network, addr string) (net.Conn, error) {
	return rt.dialUTLS(ctx, network, addr, []string{"http/1.1"})
}

// dialTLSForHTTP2 creates a TLS connection for HTTP/2 (only requests h2 via ALPN)
func (rt *UTLSRoundTripper) dialTLSForHTTP2(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
	return rt.dialUTLS(ctx, network, addr, []string{"h2"})
}

// probeALPN performs a probe connection to determine which protocol the server supports
func (rt *UTLSRoundTripper) probeALPN(ctx context.Context, addr string) (string, error) {
	// Try with both protocols to see what server prefers
	conn, err := rt.dialUTLS(ctx, "tcp", addr, []string{"h2", "http/1.1"})
	if err != nil {
		return "", err
	}
	proto := conn.ConnectionState().NegotiatedProtocol
	conn.Close()
	return proto, nil
}

// RoundTrip implements http.RoundTripper
func (rt *UTLSRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// Determine target address
	addr := req.URL.Host
	if !hasPort(addr) {
		if req.URL.Scheme == "https" {
			addr += ":443"
		} else {
			addr += ":80"
		}
	}

	// Check global protocol cache for this host
	var cachedProto string
	if v, ok := hostProtoCache.Load(addr); ok {
		cachedProto = v.(string)
	}

	// If no cached protocol, probe the server
	if cachedProto == "" {
		ctx, cancel := context.WithTimeout(req.Context(), 10*time.Second)
		proto, err := rt.probeALPN(ctx, addr)
		cancel()
		if err != nil {
			// Default to HTTP/1.1 on probe failure
			proto = "http/1.1"
		}
		hostProtoCache.Store(addr, proto)
		cachedProto = proto
		log.Printf("[TRANSPORT] Probed %s → protocol: %s", addr, proto)
	}

	// Use appropriate transport based on negotiated protocol
	if cachedProto == "h2" {
		resp, err := rt.http2Transport.RoundTrip(req)
		if err != nil {
			// Check if the error indicates the server responded with HTTP/1.1 frames
			// despite advertising h2 via ALPN. Common errors:
			//   - "frame header looked like an HTTP/1.1 header"
			//   - "INTERNAL_ERROR" or "PROTOCOL_ERROR"
			errStr := err.Error()
			if strings.Contains(errStr, "HTTP/1.1") ||
				strings.Contains(errStr, "frame") ||
				strings.Contains(errStr, "INTERNAL_ERROR") ||
				strings.Contains(errStr, "PROTOCOL_ERROR") {
				// Server lied about h2 support — downgrade globally and retry
				log.Printf("[TRANSPORT] Host %s advertised h2 but speaks HTTP/1.1, falling back globally (error: %v)", addr, err)
				hostProtoCache.Store(addr, "http/1.1")
				return rt.http1Transport.RoundTrip(req)
			}
			return nil, err
		}
		return resp, nil
	}
	return rt.http1Transport.RoundTrip(req)
}

// hasPort checks if the address already has a port
func hasPort(addr string) bool {
	_, _, err := net.SplitHostPort(addr)
	return err == nil
}

// GetCachedProto returns the cached protocol for a host (e.g. "h2" or "http/1.1").
// Returns empty string if the host hasn't been probed yet.
// This allows other packages to check the actual upstream protocol.
func GetCachedProto(host string) string {
	// Try with :443 suffix (most common for HTTPS)
	if v, ok := hostProtoCache.Load(host + ":443"); ok {
		return v.(string)
	}
	// Try exact match
	if v, ok := hostProtoCache.Load(host); ok {
		return v.(string)
	}
	return ""
}

// CreateMITMUpstreamTransport creates a transport specifically for MITM upstream connections
// with browser-like TLS fingerprint to bypass Cloudflare
func CreateMITMUpstreamTransport(host string, fingerprint BrowserFingerprint) *http.Transport {
	// For the upstream transport, we need a simple http.Transport wrapper
	// that delegates to our UTLSRoundTripper
	rt := NewUTLSRoundTripper(host, fingerprint)

	// Create a wrapper transport that uses our round tripper
	return &http.Transport{
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			// Use HTTP/1.1 by default for the Transport interface
			// The actual protocol negotiation happens in RoundTrip
			return rt.dialTLSForHTTP1(ctx, network, addr)
		},
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
}

// GetUTLSRoundTripper returns a UTLSRoundTripper for the given host
// This is the preferred way to make HTTP requests with browser TLS fingerprint
func GetUTLSRoundTripper(host string, fingerprint BrowserFingerprint) http.RoundTripper {
	return NewUTLSRoundTripper(host, fingerprint)
}

// DialUTLS creates a direct uTLS connection for WebSocket or other raw connections
func DialUTLS(ctx context.Context, addr, serverName string, fingerprint BrowserFingerprint) (net.Conn, error) {
	rt := &UTLSRoundTripper{
		fingerprint: fingerprint,
		serverName:  serverName,
		dialer: &net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		},
	}
	// For raw connections (WebSocket), use HTTP/1.1 ALPN
	return rt.dialUTLS(ctx, "tcp", addr, []string{"http/1.1"})
}
