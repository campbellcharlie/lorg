package rawproxy

import "testing"

// TestHostFromAuthority locks down the CONNECT-authority port strip, including
// the bracketed-IPv6 case that a naive first-colon split mangled.
func TestHostFromAuthority(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"example.com:443", "example.com"},
		{"example.com:8443", "example.com"},
		{"127.0.0.1:8443", "127.0.0.1"},
		{"[::1]:8443", "::1"},                       // IPv6 with port — was mangled to "["
		{"[2001:db8::1]:443", "2001:db8::1"},        // full IPv6
		{"example.com", "example.com"},              // host-only (no port) — unchanged
		{"[::1]", "[::1]"},                          // bracketed host-only — unchanged
	}
	for _, c := range cases {
		if got := hostFromAuthority(c.in); got != c.want {
			t.Errorf("hostFromAuthority(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
