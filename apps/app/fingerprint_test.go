package app

import "testing"

func TestComputeFingerprint_StableForEqualResponses(t *testing.T) {
	a := ComputeFingerprint(200, "application/json", []byte(`{"id":1,"name":"alice"}`))
	b := ComputeFingerprint(200, "application/json", []byte(`{"id":2,"name":"bob"}`))
	if a != b {
		t.Errorf("equal-shape JSON responses should fingerprint the same\n  a=%s\n  b=%s", a, b)
	}
}

func TestComputeFingerprint_DifferentForDifferentShapes(t *testing.T) {
	a := ComputeFingerprint(200, "application/json", []byte(`{"id":1}`))
	b := ComputeFingerprint(200, "application/json", []byte(`{"id":1,"admin":true}`))
	if a == b {
		t.Errorf("different-shape JSON should produce different fingerprints (got %s for both)", a)
	}
}

func TestComputeFingerprint_DifferentForDifferentStatuses(t *testing.T) {
	body := []byte(`{"ok":true}`)
	a := ComputeFingerprint(200, "application/json", body)
	b := ComputeFingerprint(403, "application/json", body)
	if a == b {
		t.Error("different status codes should not collide")
	}
}

func TestComputeFingerprint_DifferentForDifferentMimes(t *testing.T) {
	a := ComputeFingerprint(200, "application/json", []byte(`{"x":1}`))
	b := ComputeFingerprint(200, "text/html", []byte(`{"x":1}`))
	if a == b {
		t.Error("different mime buckets should not collide")
	}
}

func TestComputeFingerprint_HTML_TagShape(t *testing.T) {
	a := ComputeFingerprint(200, "text/html", []byte(`<html><body><h1>hello alice</h1></body></html>`))
	b := ComputeFingerprint(200, "text/html", []byte(`<html><body><h1>hello bob</h1></body></html>`))
	if a != b {
		t.Errorf("equal-shape HTML should fingerprint the same\n  a=%s\n  b=%s", a, b)
	}
	c := ComputeFingerprint(200, "text/html", []byte(`<html><body><form><input/></form></body></html>`))
	if a == c {
		t.Error("structurally different HTML should produce different fingerprints")
	}
}

func TestComputeFingerprint_LengthBucketing(t *testing.T) {
	// Same shape, hugely different size -> different length bucket -> different fp.
	small := ComputeFingerprint(200, "text/plain", []byte("x"))
	huge := ComputeFingerprint(200, "text/plain", make([]byte, 100_000))
	if small == huge {
		t.Error("different length buckets should not collide for non-structural content")
	}
}

func TestComputeFingerprint_FormatStable(t *testing.T) {
	fp := ComputeFingerprint(404, "application/json; charset=utf-8", []byte(`{"err":"not found"}`))
	// Must look like s<num>-m<word>-l<num>-h<hex>
	if len(fp) < 12 {
		t.Fatalf("fingerprint too short: %q", fp)
	}
	if fp[0] != 's' {
		t.Errorf("expected status prefix, got %q", fp)
	}
}
