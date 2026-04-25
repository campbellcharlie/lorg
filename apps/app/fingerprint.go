package app

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ComputeFingerprint produces a short, deterministic fingerprint string for an
// HTTP response. The format is:
//
//	s<status>-m<mime>-l<bucket>-h<hash>
//
// e.g. "s200-mjson-l3-ha7f2c91d". Responses that share the same fingerprint
// are very likely the "same kind" of response (same status, same content
// shape, same approximate size). The hash is a structural fingerprint of the
// body — for JSON we hash the sorted top-level key set; for HTML we hash a
// stable approximation of the tag skeleton; for everything else we hash the
// length bucket alone, so unrelated text payloads still cluster by size.
//
// The function is stateless and cheap: O(n) over the body for the structural
// hash. It is intended to be called inline on every saved response.
func ComputeFingerprint(status int, contentType string, body []byte) string {
	mimeBucket := mimeBucketOf(contentType)
	lengthBucket := lengthBucketOf(len(body))
	bodyHash := bodyStructuralHash(mimeBucket, body)

	return fmt.Sprintf("s%d-m%s-l%d-h%s", status, mimeBucket, lengthBucket, bodyHash)
}

// mimeBucketOf collapses content-type into a short coarse bucket. Charset
// parameters and vendor-prefixed media types are normalized away so that
// e.g. "application/json; charset=utf-8" and "application/vnd.api+json"
// both bucket to "json".
func mimeBucketOf(contentType string) string {
	ct := strings.ToLower(contentType)
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	ct = strings.TrimSpace(ct)

	switch {
	case ct == "":
		return "none"
	case strings.Contains(ct, "json"):
		return "json"
	case strings.Contains(ct, "html"):
		return "html"
	case strings.Contains(ct, "xml"):
		return "xml"
	case strings.Contains(ct, "javascript") || strings.Contains(ct, "ecmascript"):
		return "js"
	case strings.HasPrefix(ct, "text/css"):
		return "css"
	case strings.HasPrefix(ct, "text/"):
		return "text"
	case strings.HasPrefix(ct, "image/"):
		return "image"
	case strings.HasPrefix(ct, "application/x-www-form-urlencoded"):
		return "form"
	case strings.HasPrefix(ct, "multipart/"):
		return "multipart"
	default:
		return "bin"
	}
}

// lengthBucketOf returns a small integer bucket for body length. Buckets are
// roughly log-scaled so very-different sizes do not cluster together but
// near-equal sizes do.
//
//	0:  empty
//	1:  1..255
//	2:  256..1023
//	3:  1024..4095
//	4:  4096..16383
//	5:  16384..65535
//	6:  65536..262143
//	7:  >= 262144
func lengthBucketOf(n int) int {
	switch {
	case n <= 0:
		return 0
	case n < 256:
		return 1
	case n < 1024:
		return 2
	case n < 4096:
		return 3
	case n < 16384:
		return 4
	case n < 65536:
		return 5
	case n < 262144:
		return 6
	default:
		return 7
	}
}

// bodyStructuralHash produces a short hex digest representing the structural
// shape of the body. The strategy depends on the mime bucket:
//
//   - json: walk the parsed value, build a sorted list of dotted key paths
//     (depth-capped), hash that. Two JSON responses with the same set of
//     keys (regardless of values) hash the same.
//   - html: extract the lowercase tag name sequence, deduplicated and
//     truncated, hash that. Two HTML responses with the same tag set hash
//     the same.
//   - everything else: hash a constant marker, since any structural detail
//     would just bucket by exact byte content (which defeats clustering).
//
// The output is the first 8 hex chars of SHA-1, which is enough to make
// accidental collisions vanishingly rare across a single project's traffic.
func bodyStructuralHash(mimeBucket string, body []byte) string {
	const skeletonHashLen = 8

	skeleton := ""
	switch mimeBucket {
	case "json":
		skeleton = jsonSkeleton(body)
	case "html", "xml":
		skeleton = htmlTagSkeleton(body)
	default:
		skeleton = "."
	}

	if skeleton == "" {
		// Fall back to a marker so empty / unparseable bodies still land in
		// a stable bucket per mime+length combination.
		skeleton = "/"
	}

	sum := sha1.Sum([]byte(skeleton))
	return hex.EncodeToString(sum[:])[:skeletonHashLen]
}

// jsonSkeleton parses body as JSON and returns a sorted list of dotted key
// paths (e.g. "user.id,user.name,token"), depth-capped to keep the result
// bounded. Returns "" if the body is not valid JSON.
func jsonSkeleton(body []byte) string {
	const maxDepth = 4
	const maxKeys = 256

	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return ""
	}

	var paths []string
	var walk func(node any, prefix string, depth int)
	walk = func(node any, prefix string, depth int) {
		if len(paths) >= maxKeys {
			return
		}
		if depth > maxDepth {
			return
		}
		switch x := node.(type) {
		case map[string]any:
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				p := k
				if prefix != "" {
					p = prefix + "." + k
				}
				paths = append(paths, p)
				walk(x[k], p, depth+1)
				if len(paths) >= maxKeys {
					return
				}
			}
		case []any:
			// Don't index into arrays — we want the SHAPE, not the cardinality.
			// Walk only the first element to learn array element shape.
			if len(x) > 0 {
				walk(x[0], prefix+"[]", depth+1)
			}
		}
	}
	walk(v, "", 0)
	sort.Strings(paths)
	return strings.Join(paths, ",")
}

// htmlTagSkeleton scans body and returns a sorted, deduplicated list of
// lowercase tag names found in opening tags (e.g. "body,div,html,p,script").
// Returns "" if no tags are found.
func htmlTagSkeleton(body []byte) string {
	const maxTags = 64

	tags := make(map[string]struct{})
	for i := 0; i < len(body) && len(tags) < maxTags; i++ {
		if body[i] != '<' {
			continue
		}
		i++
		if i >= len(body) {
			break
		}
		// Skip closing tags, comments, doctype, processing instructions —
		// they don't add new shape information beyond the opening tags.
		if body[i] == '/' || body[i] == '!' || body[i] == '?' {
			continue
		}
		// Read tag name
		start := i
		for i < len(body) && body[i] != ' ' && body[i] != '>' && body[i] != '\t' && body[i] != '\n' && body[i] != '/' {
			i++
		}
		if i <= start {
			continue
		}
		name := strings.ToLower(string(body[start:i]))
		// Reject anything that doesn't look like a tag name to avoid eating
		// JS comparisons like "<count" inside a script body.
		if !isPlausibleTagName(name) {
			continue
		}
		tags[name] = struct{}{}
	}

	if len(tags) == 0 {
		return ""
	}
	out := make([]string, 0, len(tags))
	for k := range tags {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func isPlausibleTagName(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
			return false
		}
	}
	// Must start with a letter.
	c := s[0]
	return c >= 'a' && c <= 'z'
}
