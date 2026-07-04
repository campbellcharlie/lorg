package rawproxy

import (
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"testing"
)

func TestComputeClientHelloJA4(t *testing.T) {
	hello := &tls.ClientHelloInfo{
		CipherSuites:      []uint16{0x0a0a, 0x1303, 0x1301, 0x1302},
		ServerName:        "app.example.com",
		SignatureSchemes:  []tls.SignatureScheme{0x0403, 0x0804},
		SupportedProtos:   []string{"h2", "http/1.1"},
		SupportedVersions: []uint16{tls.VersionTLS12, tls.VersionTLS13},
		Extensions:        []uint16{0x0010, 0x0000, 0x0a0a, 0x000d, 0x000a},
	}

	got := ComputeClientHelloJA4("app.example.com", hello)

	const want = "t13d0304h2_55b375c5d22e_20ea43ed96b2"
	if got.JA4 != want {
		t.Fatalf("JA4 mismatch: got %q want %q", got.JA4, want)
	}
	if got.Raw != "t13d0304h2_1301,1302,1303_0000,000a,000d,0010_0403,0804" {
		t.Fatalf("raw mismatch: %q", got.Raw)
	}
	if got.CipherCount != 3 || got.ExtensionCount != 4 {
		t.Fatalf("counts mismatch: ciphers=%d extensions=%d", got.CipherCount, got.ExtensionCount)
	}
}

func TestComputeClientHelloJA4NoSNI(t *testing.T) {
	hello := &tls.ClientHelloInfo{
		CipherSuites:      []uint16{0x1302, 0x1301},
		SignatureSchemes:  []tls.SignatureScheme{0x0804},
		SupportedVersions: []uint16{tls.VersionTLS13},
		Extensions:        []uint16{0x0033, 0x002b},
	}

	got := ComputeClientHelloJA4("10.0.0.1", hello)

	const want = "t13i020200_62ed6f6ca7ad_39dcdb5634d2"
	if got.JA4 != want {
		t.Fatalf("JA4 mismatch: got %q want %q", got.JA4, want)
	}
}

func TestComputeTLSStateHashStable(t *testing.T) {
	got := ComputeTLSStateHash("example.com", TLSStateSnapshot{
		Version:            tls.VersionTLS13,
		CipherSuite:        tls.TLS_AES_128_GCM_SHA256,
		NegotiatedProtocol: "h2",
		PeerCertificates: []*x509.Certificate{{
			DNSNames: []string{"www.example.com"},
			Subject:  pkix.Name{CommonName: "example.com"},
		}},
	})

	const want = "9b36866df67b474a8b98a554"
	if got.StateHash != want {
		t.Fatalf("state hash mismatch: got %q want %q", got.StateHash, want)
	}
	if got.TLSVersion != "TLS1.3" {
		t.Fatalf("TLS version mismatch: %s", got.TLSVersion)
	}
}
