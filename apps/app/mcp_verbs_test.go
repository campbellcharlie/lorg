package app

import "testing"

func TestNormalizePathTemplate(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"/api/users/123", "/api/users/{id}"},
		{"/api/users/0", "/api/users/{id}"},
		{"/api/users/abc", "/api/users/abc"},
		{"/api/users/12/posts/456", "/api/users/{id}/posts/{id}"},
		{"/api/items/550e8400-e29b-41d4-a716-446655440000", "/api/items/{uuid}"},
		// 16+ char hex blob -> {hex}; shorter stays literal.
		{"/files/deadbeefdeadbeef", "/files/{hex}"},
		{"/files/abcdef", "/files/abcdef"},
		{"/", "/"},
		{"/static/index.html", "/static/index.html"},
	}
	for _, c := range cases {
		got := normalizePathTemplate(c.in)
		if got != c.want {
			t.Errorf("normalizePathTemplate(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDetectAuthMechanism(t *testing.T) {
	cases := []struct {
		name, raw, want string
	}{
		{"bearer", "GET /x HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer abc.def.ghi\r\n\r\n", "Bearer"},
		{"basic", "GET /x HTTP/1.1\r\nHost: x\r\nAuthorization: Basic dXNlcjpwYXNz\r\n\r\n", "Basic"},
		{"other-auth", "GET /x HTTP/1.1\r\nHost: x\r\nAuthorization: Negotiate xyz\r\n\r\n", "Authorization"},
		{"x-api-key", "GET /x HTTP/1.1\r\nHost: x\r\nX-API-Key: abc\r\n\r\n", "APIKey"},
		{"api-key", "GET /x HTTP/1.1\r\nHost: x\r\nApi-Key: abc\r\n\r\n", "APIKey"},
		{"cookie", "GET /x HTTP/1.1\r\nHost: x\r\nCookie: sid=abc\r\n\r\n", "Cookie"},
		{"x-auth-token", "GET /x HTTP/1.1\r\nHost: x\r\nX-Auth-Token: abc\r\n\r\n", "AuthToken"},
		{"none", "GET /x HTTP/1.1\r\nHost: x\r\n\r\n", ""},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := detectAuthMechanism(c.raw)
			if got != c.want {
				t.Errorf("detectAuthMechanism = %q, want %q", got, c.want)
			}
		})
	}
}

func TestIsAllDigits(t *testing.T) {
	if !isAllDigits("123") {
		t.Error("123 should be digits")
	}
	if isAllDigits("12a") {
		t.Error("12a should not be digits")
	}
	if isAllDigits("") {
		t.Error("empty should not be digits")
	}
}

func TestIsHexBlob(t *testing.T) {
	if !isHexBlob("deadbeefdeadbeef") {
		t.Error("16-char hex should match")
	}
	if isHexBlob("deadbeef") {
		t.Error("8-char hex should not match (too short)")
	}
	if isHexBlob("not-hex-just-words-that-are-long") {
		t.Error("non-hex shouldn't match even if long")
	}
}
