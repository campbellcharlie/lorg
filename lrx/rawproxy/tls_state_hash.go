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

// tlsStateHashCache stores computed TLS state hashes keyed by host.
var tlsStateHashCache sync.Map // map[string]TLSStateHash

// TLSStateHash holds a post-handshake state hash derived from the
// server-side TLS connection state observed after a uTLS handshake.
type TLSStateHash struct {
	Host        string `json:"host"`
	StateHash   string `json:"stateHash"`
	TLSVersion  string `json:"tlsVersion"`
	CipherSuite string `json:"cipherSuite"`
	ALPN        string `json:"alpn"`
	ServerName  string `json:"serverName"`
}

// ComputeTLSStateHash builds a deterministic hash from a TLS connection state.
func ComputeTLSStateHash(host string, state TLSStateSnapshot) TLSStateHash {
	fp := TLSStateHash{
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
	fp.StateHash = fmt.Sprintf("%x", hash[:12])

	return fp
}

// TLSStateSnapshot is a minimal subset of TLS connection state fields needed
// for state-hash computation. This avoids coupling to either crypto/tls or utls
// ConnectionState types directly.
type TLSStateSnapshot struct {
	Version            uint16
	CipherSuite        uint16
	NegotiatedProtocol string
	ServerName         string
	PeerCertificates   []*x509.Certificate
}

// CacheTLSStateHash stores a TLS state hash for a host.
func CacheTLSStateHash(host string, fp TLSStateHash) {
	tlsStateHashCache.Store(host, fp)
}

// GetTLSStateHash retrieves a cached TLS state hash for a host.
func GetTLSStateHash(host string) (TLSStateHash, bool) {
	val, ok := tlsStateHashCache.Load(host)
	if !ok {
		return TLSStateHash{}, false
	}
	return val.(TLSStateHash), true
}

// GetAllTLSStateHash returns all cached TLS state hashes sorted by host.
func GetAllTLSStateHash() []TLSStateHash {
	var results []TLSStateHash
	tlsStateHashCache.Range(func(key, value any) bool {
		results = append(results, value.(TLSStateHash))
		return true
	})
	sort.Slice(results, func(i, j int) bool {
		return results[i].Host < results[j].Host
	})
	return results
}
