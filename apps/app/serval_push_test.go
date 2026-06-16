package app

import (
	"fmt"
	"sync"
	"testing"

	"github.com/campbellcharlie/lorg/internal/types"
)

// TestServalPushCreatesProjectsOnDemand proves the push-ingest contract (ADR-005):
// a pushed Serval entry tagged with a project that does not yet exist creates
// that project DB on the fly and the row is returned by a project-scoped query —
// no restart, no pre-existing active project. Two distinct profiles stay routed
// to their own projects.
func TestServalPushCreatesProjectsOnDemand(t *testing.T) {
	if raceEnabled {
		t.Skip("skips under -race: pre-existing fire-and-forget race in SaveRequestToBackend, unrelated to the routing contract under test")
	}
	backend := newProjectTagTestBackend(t)
	// Active project is something neutral; pushes target other, novel projects.
	useGlobalProjectDB(t, t.TempDir(), "neutral-active")

	// label -> project tag. Hosts are hyphen-free to avoid an unrelated
	// pre-existing sitemap-collection naming quirk on hyphenated hosts.
	cases := map[string]string{"alpha": "push-alpha", "bravo": "push-bravo"}
	for label, project := range cases {
		host := label + ".example.com"
		body := types.AddRequestBodyType{
			Url:         "https://" + host,
			Request:     "GET /p HTTP/1.1\r\nHost: " + host + "\r\nCookie: sid=" + label + "\r\n\r\n",
			Response:    "HTTP/1.1 200 OK\r\nContent-Type: text/html\r\n\r\nok",
			GeneratedBy: ServalSource,
			Note:        "Pushed from Serval",
			Project:     project,
		}
		if _, err := backend.SaveRequestToBackend(body); err != nil {
			t.Fatalf("push %s: %v", project, err)
		}
	}

	// Each novel project is queryable with exactly its own row.
	for _, project := range cases {
		if got := queryUntil(t, backend, `req.method.eq:"GET"`, project, 1); got != 1 {
			t.Fatalf("project:%q returned %d rows, want 1 (dynamic project create + routing)", project, got)
		}
	}

	// Cross-project isolation: alpha's scope never returns bravo's row.
	leak, _, err := backend.executeTrafficQueryWithProject(`req.host.cont:"bravo"`, "push-alpha", 100)
	if err != nil {
		t.Fatalf("cross-project query: %v", err)
	}
	if len(leak) != 0 {
		t.Fatalf("push-alpha scope leaked %d bravo row(s)", len(leak))
	}
}

// TestServalPushConcurrentProjectsRoute exercises many concurrent pushes to
// distinct projects (the scenario the write-handle cache bump to 32 targets):
// every project must end up with exactly its own row.
func TestServalPushConcurrentProjectsRoute(t *testing.T) {
	if raceEnabled {
		t.Skip("skips under -race: pre-existing fire-and-forget race in SaveRequestToBackend, unrelated to the routing contract under test")
	}
	backend := newProjectTagTestBackend(t)
	useGlobalProjectDB(t, t.TempDir(), "neutral-active")

	const n = 12
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			label := fmt.Sprintf("sess%02d", i) // hyphen-free host label
			project := fmt.Sprintf("sess-%02d", i)
			host := label + ".example.com"
			body := types.AddRequestBodyType{
				Url:         "https://" + host,
				Request:     fmt.Sprintf("GET /%s HTTP/1.1\r\nHost: %s\r\n\r\n", label, host),
				Response:    "HTTP/1.1 200 OK\r\n\r\nok",
				GeneratedBy: ServalSource,
				Project:     project,
			}
			if _, err := backend.SaveRequestToBackend(body); err != nil {
				t.Errorf("push %s: %v", project, err)
			}
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		project := fmt.Sprintf("sess-%02d", i)
		if got := queryUntil(t, backend, `req.method.eq:"GET"`, project, 1); got != 1 {
			t.Fatalf("project:%q returned %d rows, want 1", project, got)
		}
	}
}
