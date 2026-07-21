package rawproxy

import (
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/hex"
	"strings"
)

// sha12 matches FoxIO's sha_encode: the first 12 hex characters (6 bytes) of the
// SHA-256 of the input. Unlike lorg's truncatedSHA256 it does NOT zero-fill the
// empty string — JA4H/JA4X hash the joined value directly, so an empty input
// hashes to sha256("")[:12]. (Only JA4H's no-cookie case uses an explicit 0x12,
// handled at the call site, mirroring ja4h.py.)
func sha12(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:6])
}

// oidToHex encodes a dotted OID to the hex of its DER *value* bytes (no tag or
// length prefix), matching FoxIO ja4x.oid_to_hex — e.g. 2.5.4.6 -> "550406",
// 2.5.4.10 -> "55040a", 1.2.840.113549 -> "2a864886f70d".
func oidToHex(oid asn1.ObjectIdentifier) string {
	if len(oid) < 2 {
		return ""
	}
	b := []byte{byte(oid[0]*40 + oid[1])}
	for _, arc := range oid[2:] {
		b = append(b, vlq(arc)...)
	}
	return hex.EncodeToString(b)
}

// vlq base-128 encodes an OID arc: the high bit is set on every byte except the
// least-significant one (big-endian), matching ASN.1 OID subidentifier encoding.
func vlq(v int) []byte {
	out := []byte{byte(v & 0x7f)}
	for v >>= 7; v > 0; v >>= 7 {
		out = append([]byte{byte(v&0x7f | 0x80)}, out...)
	}
	return out
}

// rdnOIDHexes returns the DER-value hexes of every attribute OID in a raw RDN
// sequence (RawIssuer/RawSubject), in the order they appear in the certificate.
func rdnOIDHexes(raw []byte) []string {
	var seq pkix.RDNSequence
	if _, err := asn1.Unmarshal(raw, &seq); err != nil {
		return nil
	}
	var out []string
	for _, rdn := range seq {
		for _, atv := range rdn {
			out = append(out, oidToHex(atv.Type))
		}
	}
	return out
}

// ComputeJA4X computes the FoxIO JA4X fingerprint for one x509 certificate:
//
//	sha12(issuer attr OIDs) _ sha12(subject attr OIDs) _ sha12(extension OIDs)
//
// each OID rendered as its DER-value hex, in certificate order. All inputs come
// from the cert lorg already holds (RawIssuer/RawSubject/Extensions), so this is
// a pure, no-extra-capture computation.
func ComputeJA4X(cert *x509.Certificate) string {
	issuers := rdnOIDHexes(cert.RawIssuer)
	subjects := rdnOIDHexes(cert.RawSubject)
	exts := make([]string, 0, len(cert.Extensions))
	for _, e := range cert.Extensions {
		exts = append(exts, oidToHex(e.Id))
	}
	return sha12(strings.Join(issuers, ",")) + "_" +
		sha12(strings.Join(subjects, ",")) + "_" +
		sha12(strings.Join(exts, ","))
}
