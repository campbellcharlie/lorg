package app

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/campbellcharlie/lorg/internal/types"
)

// TestLogTrafficConcurrentAcrossProjects is the ADR-003 A1 proof: many goroutines
// logging concurrently to several different project handles all complete and ALL
// rows land — no lost writes, no deadlock, no data race (run with -race). Under
// the old single-mutex-held-across-I/O design these serialized; the row-count
// assertion guards that narrowing the lock didn't drop any.
func TestLogTrafficConcurrentAcrossProjects(t *testing.T) {
	dir := t.TempDir()
	p := &ProjectDB{}
	if err := p.Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := p.SetProject("Active", dir); err != nil {
		t.Fatalf("SetProject: %v", err)
	}

	const projects = 4
	const perProject = 50
	names := []string{"", "ProjOne", "ProjTwo", "ProjThree"} // "" = Active handle

	var wg sync.WaitGroup
	for pi := 0; pi < projects; pi++ {
		for i := 0; i < perProject; i++ {
			wg.Add(1)
			go func(project string, n int) {
				defer wg.Done()
				ud := types.UserData{
					Host:        "example.com",
					Port:        "443",
					IsHTTPS:     true,
					GeneratedBy: "proxy/http",
					ReqJson:     types.RequestData{Method: "GET", Path: fmt.Sprintf("/p%d", n)},
					RespJson:    types.ResponseData{Status: 200},
					Project:     project,
				}
				if err := p.LogTraffic(ud, "GET / HTTP/1.1\r\nHost: example.com\r\n\r\n",
					"HTTP/1.1 200 OK\r\n\r\nok"); err != nil {
					t.Errorf("LogTraffic(%q): %v", project, err)
				}
			}(names[pi], i)
		}
	}
	wg.Wait()

	p.Close()
	// "" routed to the Active handle (Active.db); the rest to their own files.
	checks := map[string]int{
		"Active.db":    perProject,
		"ProjOne.db":   perProject,
		"ProjTwo.db":   perProject,
		"ProjThree.db": perProject,
	}
	for file, want := range checks {
		if got := countTraffic(t, filepath.Join(dir, file)); got != want {
			t.Errorf("%s: %d rows, want %d", file, got, want)
		}
	}
}

// TestWriteHandleRegistryBounded is the ADR-003 A2 proof: addressing more
// projects than the cap keeps the open-handle registry bounded (FIFO eviction),
// and rows written to a project before its handle was evicted still persist on
// disk — eviction closes the handle, it does not lose data.
func TestWriteHandleRegistryBounded(t *testing.T) {
	dir := t.TempDir()
	p := &ProjectDB{}
	if err := p.Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := p.SetProject("Active", dir); err != nil {
		t.Fatalf("SetProject: %v", err)
	}

	const total = maxWriteHandles + 6
	for i := 0; i < total; i++ {
		proj := fmt.Sprintf("Eng%02d", i)
		ud := types.UserData{
			Host: "example.com", Port: "443", IsHTTPS: true, GeneratedBy: "proxy/http",
			ReqJson:  types.RequestData{Method: "GET", Path: "/x"},
			RespJson: types.ResponseData{Status: 200},
			Project:  proj,
		}
		if err := p.LogTraffic(ud, "GET / HTTP/1.1\r\nHost: example.com\r\n\r\n", "HTTP/1.1 200 OK\r\n\r\nok"); err != nil {
			t.Fatalf("LogTraffic(%s): %v", proj, err)
		}
	}

	// Registry is capped...
	p.mu.RLock()
	open := len(p.writeHandles)
	p.mu.RUnlock()
	if open > maxWriteHandles {
		t.Errorf("registry holds %d handles, want <= %d", open, maxWriteHandles)
	}

	// ...but every project's row persisted, including evicted ones.
	p.Close()
	for i := 0; i < total; i++ {
		f := filepath.Join(dir, fmt.Sprintf("Eng%02d.db", i))
		if got := countTraffic(t, f); got != 1 {
			t.Errorf("Eng%02d.db has %d rows, want 1 (eviction must not lose data)", i, got)
		}
	}
}
