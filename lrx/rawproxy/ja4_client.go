package rawproxy

import (
	"crypto/sha256"
	"crypto/tls"
	"fmt"
	"sort"
	"strings"
	"sync"
)

var clientJA4Cache sync.Map // map[string]ClientJA4

type ClientJA4 struct {
	Host            string   `json:"host"`
	JA4            string   `json:"ja4"`
	Raw            string   `json:"raw"`
	TLSVersion     string   `json:"tlsVersion"`
	SNI            bool     `json:"sni"`
	ALPN           string   `json:"alpn"`
	CipherCount    int      `json:"cipherCount"`
	ExtensionCount int      `json:"extensionCount"`
	Ciphers         []string `json:"ciphers,omitempty"`
	Extensions      []string `json:"extensions,omitempty"`
	SignatureAlgs   []string `json:"signatureAlgorithms,omitempty"`
}

func ComputeClientHelloJA4(host string, hello *tls.ClientHelloInfo) ClientJA4 {
	fp := ClientJA4{
		Host:            host,
		SNI:             hello.ServerName != "",
		CipherCount:    len(nonGREASEUint16s(hello.CipherSuites)),
		ExtensionCount: len(nonGREASEUint16s(hello.Extensions)),
	}
	if len(hello.SupportedProtos) > 0 {
		fp.ALPN = hello.SupportedProtos[0]
	}

	version := maxTLSVersion(hello.SupportedVersions)
	fp.TLSVersion = ja4TLSVersion(version)

	ciphers := nonGREASEHex(hello.CipherSuites)
	extensions := nonGREASEHex(hello.Extensions)
	signatureAlgs := signatureSchemeHex(hello.SignatureSchemes)
	fp.Ciphers = append([]string(nil), ciphers...)
	fp.Extensions = append([]string(nil), extensions...)
	fp.SignatureAlgs = append([]string(nil), signatureAlgs...)

	a := fmt.Sprintf("t%s%s%02d%02d%s",
		fp.TLSVersion,
		ja4SNIFlag(fp.SNI),
		fp.CipherCount,
		fp.ExtensionCount,
		ja4ALPN(fp.ALPN),
	)

	b := truncatedSHA256(strings.Join(ciphers, ","))

	cInput := strings.Join(extensions, ",")
	if len(signatureAlgs) > 0 {
		cInput += "_" + strings.Join(signatureAlgs, ",")
	}
	c := truncatedSHA256(cInput)

	fp.Raw = a + "_" + strings.Join(ciphers, ",") + "_" + cInput
	fp.JA4 = a + "_" + b + "_" + c
	return fp
}

func CacheClientJA4(host string, fp ClientJA4) {
	clientJA4Cache.Store(host, fp)
}

func GetClientJA4(host string) (ClientJA4, bool) {
	val, ok := clientJA4Cache.Load(host)
	if !ok {
		return ClientJA4{}, false
	}
	return val.(ClientJA4), true
}

func GetAllClientJA4() []ClientJA4 {
	var results []ClientJA4
	clientJA4Cache.Range(func(key, value any) bool {
		results = append(results, value.(ClientJA4))
		return true
	})
	sort.Slice(results, func(i, j int) bool {
		return results[i].Host < results[j].Host
	})
	return results
}

func maxTLSVersion(versions []uint16) uint16 {
	var max uint16
	for _, v := range versions {
		if v > max {
			max = v
		}
	}
	return max
}

func ja4TLSVersion(version uint16) string {
	switch version {
	case tls.VersionTLS10:
		return "10"
	case tls.VersionTLS11:
		return "11"
	case tls.VersionTLS12:
		return "12"
	case tls.VersionTLS13:
		return "13"
	default:
		return fmt.Sprintf("%02x", version&0xff)
	}
}

func ja4SNIFlag(present bool) string {
	if present {
		return "d"
	}
	return "i"
}

func ja4ALPN(alpn string) string {
	if alpn == "" {
		return "00"
	}
	if len(alpn) == 1 {
		return alpn + alpn
	}
	return alpn[:1] + alpn[len(alpn)-1:]
}

func nonGREASEUint16s(values []uint16) []uint16 {
	out := make([]uint16, 0, len(values))
	for _, v := range values {
		if !isGREASE(v) {
			out = append(out, v)
		}
	}
	return out
}

func nonGREASEHex(values []uint16) []string {
	filtered := nonGREASEUint16s(values)
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i] < filtered[j]
	})
	out := make([]string, 0, len(filtered))
	for _, v := range filtered {
		out = append(out, fmt.Sprintf("%04x", v))
	}
	return out
}

func signatureSchemeHex(values []tls.SignatureScheme) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if !isGREASE(uint16(v)) {
			out = append(out, fmt.Sprintf("%04x", uint16(v)))
		}
	}
	return out
}

func isGREASE(v uint16) bool {
	return v&0x0f0f == 0x0a0a
}

func truncatedSHA256(s string) string {
	if s == "" {
		return "000000000000"
	}
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", sum[:])[:12]
}
