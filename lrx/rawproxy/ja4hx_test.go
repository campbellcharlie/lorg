package rawproxy

import (
	"encoding/asn1"
	"testing"
)

// Reference values independently computed from the FoxIO ja4h.py / ja4x.py
// algorithm (sha_encode = sha256(",".join)[:12], oid_to_hex = DER-value hex).

func TestOIDToHex(t *testing.T) {
	cases := []struct {
		oid  asn1.ObjectIdentifier
		want string
	}{
		{asn1.ObjectIdentifier{2, 5, 4, 6}, "550406"},         // countryName
		{asn1.ObjectIdentifier{2, 5, 4, 10}, "55040a"},        // organizationName
		{asn1.ObjectIdentifier{2, 5, 4, 3}, "550403"},         // commonName
		{asn1.ObjectIdentifier{2, 5, 29, 15}, "551d0f"},       // keyUsage
		{asn1.ObjectIdentifier{1, 2, 840, 113549}, "2a864886f70d"}, // RSA (multi-byte arcs)
	}
	for _, c := range cases {
		if got := oidToHex(c.oid); got != c.want {
			t.Errorf("oidToHex(%v) = %q, want %q", c.oid, got, c.want)
		}
	}
}

func TestComputeJA4H(t *testing.T) {
	// No cookies, with Accept-Language. a = ge(GET) 11(HTTP/1.1) n(no cookie)
	// n(no referer) 04(host,user-agent,accept,accept-language) enus(lang).
	got := ComputeJA4H("GET", "HTTP/1.1", []string{
		"Host: x", "User-Agent: y", "Accept: z", "Accept-Language: en-US,en;q=0.9",
	})
	want := "ge11nn04enus_171d872ea17d_000000000000_000000000000"
	if got != want {
		t.Errorf("JA4H (no cookie) = %q, want %q", got, want)
	}

	// Cookie present: c/d become the sorted name / name=value hashes; cookie and
	// referer headers are excluded from the count and the header hash.
	got = ComputeJA4H("GET", "HTTP/1.1", []string{"Host: x", "Cookie: b=2; a=1"})
	// a = ge 11 c(cookie) n(no referer) 01(host only) 0000(no lang);
	// b = sha12("host"); c = sha12("a,b"); d = sha12("a=1,b=2")
	want = "ge11cn010000_4740ae6347b0_1eb7c54d5283_06beefe2b477"
	if got != want {
		t.Errorf("JA4H (cookie) = %q, want %q", got, want)
	}
}

func TestComputeJA4H_HTTP2(t *testing.T) {
	// http2 -> version "20"; a leading ":authority" pseudo-header is skipped.
	got := ComputeJA4H("POST", "http2", []string{":authority: x", "Host: x"})
	// filtered names = [host] -> "01"; b = sha12("host")
	want := "po20nn010000_4740ae6347b0_000000000000_000000000000"
	if got != want {
		t.Errorf("JA4H (http2) = %q, want %q", got, want)
	}
}
