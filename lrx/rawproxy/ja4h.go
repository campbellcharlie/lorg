package rawproxy

import (
	"fmt"
	"sort"
	"strings"
)

// ComputeJA4H computes the FoxIO JA4H fingerprint from a client HTTP request,
// following the reference ja4h.py exactly:
//
//	a = {method[:2]}{version}{c|n}{r|n}{headerCount:02}{lang4}
//	b = sha12(header names in order, excluding pseudo/cookie/referer/empty)
//	c = sha12(sorted cookie names)         or 000000000000 when no cookies
//	d = sha12(sorted "name=value" cookies) or 000000000000 when no cookies
//	JA4H = a_b_c_d
//
// method is the HTTP method; httpVersion is "1.0"/"1.1"/"2"/"http2"; headerLines
// are the request header lines in wire order, each "Name: value". lorg is the
// client, so every input is present in its own outgoing request — no capture.
func ComputeJA4H(method, httpVersion string, headerLines []string) string {
	a := ja4hMethod(method) + ja4hVersion(httpVersion)

	var names []string
	var cookieHeader, langValue string
	hasCookie, hasReferer := false, false

	for _, line := range headerLines {
		if strings.HasPrefix(line, ":") { // HTTP/2 pseudo-header
			continue
		}
		name, value := line, ""
		if i := strings.IndexByte(line, ':'); i >= 0 {
			name, value = strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:])
		}
		lname := strings.ToLower(name)
		switch {
		case lname == "":
			continue
		case lname == "cookie":
			hasCookie = true
			cookieHeader = value
		case lname == "referer":
			hasReferer = true
		default:
			names = append(names, lname)
			if lname == "accept-language" && langValue == "" {
				langValue = value
			}
		}
	}

	a += boolFlag(hasCookie, 'c') + boolFlag(hasReferer, 'r')
	a += fmt.Sprintf("%02d", min2(len(names), 99))
	a += ja4hLang(langValue)

	b := sha12(strings.Join(names, ","))

	c, d := "000000000000", "000000000000"
	if hasCookie {
		fields, pairs := parseCookies(cookieHeader)
		c = sha12(strings.Join(fields, ","))
		d = sha12(strings.Join(pairs, ","))
	}

	return a + "_" + b + "_" + c + "_" + d
}

// JA4HFromRawRequest parses a raw HTTP/1.x request (request line + headers, wire
// order preserved) and returns its JA4H. The body is ignored.
func JA4HFromRawRequest(raw string) (string, error) {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	lines := strings.Split(raw, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return "", fmt.Errorf("empty request")
	}
	parts := strings.Fields(lines[0]) // METHOD request-target HTTP/x.y
	if len(parts) < 1 {
		return "", fmt.Errorf("malformed request line")
	}
	method := parts[0]
	version := "1.1"
	if len(parts) >= 3 {
		version = parts[2]
	}
	var headerLines []string
	for _, l := range lines[1:] {
		if strings.TrimSpace(l) == "" {
			break // blank line ends the header block
		}
		headerLines = append(headerLines, l)
	}
	return ComputeJA4H(method, version, headerLines), nil
}

func ja4hMethod(method string) string {
	m := strings.ToLower(method)
	if len(m) > 2 {
		m = m[:2]
	}
	return m
}

func ja4hVersion(v string) string {
	v = strings.ToLower(v)
	if v == "http2" || v == "2" || v == "2.0" || strings.HasSuffix(v, "/2") {
		return "20"
	}
	// Reduce "HTTP/1.1" / "1.1" -> "11", "1.0" -> "10".
	if i := strings.LastIndexByte(v, '/'); i >= 0 {
		v = v[i+1:]
	}
	v = strings.ReplaceAll(v, ".", "")
	if v == "" {
		return "11"
	}
	return v
}

// ja4hLang mirrors ja4h.py http_language: strip '-', turn ';' into ',', lower,
// take the first comma-separated token, first 4 chars, right-padded with '0'.
func ja4hLang(lang string) string {
	if lang == "" {
		return "0000"
	}
	lang = strings.ReplaceAll(lang, "-", "")
	lang = strings.ReplaceAll(lang, ";", ",")
	lang = strings.ToLower(lang)
	lang = strings.Split(lang, ",")[0]
	if len(lang) > 4 {
		lang = lang[:4]
	}
	return lang + strings.Repeat("0", 4-len(lang))
}

// parseCookies splits a Cookie header into names and "name=value" pairs, both
// sorted by cookie name (ja4h.py sorts by the pair's name).
func parseCookies(header string) (fields, pairs []string) {
	type kv struct{ name, pair string }
	var cs []kv
	for _, part := range strings.Split(header, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name := part
		if i := strings.IndexByte(part, '='); i >= 0 {
			name = strings.TrimSpace(part[:i])
		}
		cs = append(cs, kv{name: name, pair: part})
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].name < cs[j].name })
	for _, c := range cs {
		fields = append(fields, c.name)
		pairs = append(pairs, c.pair)
	}
	return fields, pairs
}

func boolFlag(b bool, set byte) string {
	if b {
		return string(set)
	}
	return "n"
}

func min2(a, b int) int {
	if a < b {
		return a
	}
	return b
}
