package rawproxy

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// ja4Cache stores computed JA4 fingerprints keyed by host.
var ja4Cache sync.Map // map[string]JA4Fingerprint

// JA4Fingerprint holds computed JA4+ fingerprint data derived from the
// server-side TLS connection state observed after a uTLS handshake.
type JA4Fingerprint struct {
	Host        string `json:"host"`
	JA4         string `json:"ja4"`
	TLSVersion  string `json:"tlsVersion"`
	CipherSuite string `json:"cipherSuite"`
	ALPN        string `json:"alpn"`
	ServerName  string `json:"serverName"`
}

// ComputeJA4 builds a JA4-style fingerprint from a TLS connection state.
// This captures the server-selected parameters (version, cipher, ALPN) plus
// peer certificate metadata. Full client-hello JA4 would require parsing the
// raw ClientHello which is handled internally by uTLS.
func ComputeJA4(host string, state TLSStateSnapshot) JA4Fingerprint {
	fp := JA4Fingerprint{
		Host:       host,
		ServerName: state.ServerName,
		ALPN:       state.NegotiatedProtocol,
	}

	// TLS version
	switch state.Version {
	case tls.VersionTLS10:
		fp.TLSVersion = "TLS1.0"
	case tls.VersionTLS11:
		fp.TLSVersion = "TLS1.1"
	case tls.VersionTLS12:
		fp.TLSVersion = "TLS1.2"
	case tls.VersionTLS13:
		fp.TLSVersion = "TLS1.3"
	default:
		fp.TLSVersion = fmt.Sprintf("0x%04x", state.Version)
	}

	// Cipher suite name
	fp.CipherSuite = tls.CipherSuiteName(state.CipherSuite)

	// Build JA4-style hash: version_alpn_cipher[_cert info]
	components := []string{
		fp.TLSVersion,
		fp.ALPN,
		fp.CipherSuite,
	}

	// Add peer certificate info if available
	if len(state.PeerCertificates) > 0 {
		cert := state.PeerCertificates[0]
		components = append(components, cert.Subject.CommonName)
		// Add SANs sorted for deterministic hashing
		sans := make([]string, len(cert.DNSNames))
		copy(sans, cert.DNSNames)
		sort.Strings(sans)
		if len(sans) > 0 {
			components = append(components, strings.Join(sans, ","))
		}
	}

	raw := strings.Join(components, "_")
	hash := sha256.Sum256([]byte(raw))
	fp.JA4 = fmt.Sprintf("%x", hash[:12]) // first 12 bytes = 24 hex chars

	return fp
}

// TLSStateSnapshot is a minimal subset of TLS connection state fields needed
// for JA4 computation. This avoids coupling to either crypto/tls or utls
// ConnectionState types directly.
type TLSStateSnapshot struct {
	Version            uint16
	CipherSuite        uint16
	NegotiatedProtocol string
	ServerName         string
	PeerCertificates   []*x509.Certificate
}

// CacheJA4 stores a JA4 fingerprint for a host.
func CacheJA4(host string, fp JA4Fingerprint) {
	ja4Cache.Store(host, fp)
}

// GetJA4 retrieves a cached JA4 fingerprint for a host.
func GetJA4(host string) (JA4Fingerprint, bool) {
	val, ok := ja4Cache.Load(host)
	if !ok {
		return JA4Fingerprint{}, false
	}
	return val.(JA4Fingerprint), true
}

// GetAllJA4 returns all cached JA4 fingerprints sorted by host.
func GetAllJA4() []JA4Fingerprint {
	var results []JA4Fingerprint
	ja4Cache.Range(func(key, value any) bool {
		results = append(results, value.(JA4Fingerprint))
		return true
	})
	sort.Slice(results, func(i, j int) bool {
		return results[i].Host < results[j].Host
	})
	return results
}
