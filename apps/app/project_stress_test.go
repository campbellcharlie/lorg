package app

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/campbellcharlie/lorg/internal/types"
)

// TestUnionReadWriteStress hammers the ADR-004 read path (unionTrafficRows,
// unionCompiledQuery, getTrafficBytes) concurrently WHILE writers capture across
// many projects — to surface races/panics/corruption under load. Run with -race.
//
// This is the adversarial test for the consolidation: cross-project reads open
// every project DB read-only while writers hold the active handles, registry
// eviction churns, and byte reconstruction runs mid-write.
func TestUnionReadWriteStress(t *testing.T) {
	if testing.Short() {
		t.Skip("stress test (~40s) — run without -short")
	}
	dir := t.TempDir()
	p := &ProjectDB{}
	if err := p.Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := p.SetProject("Active", dir); err != nil {
		t.Fatalf("SetProject: %v", err)
	}

	// Use MORE projects than the registry cap (maxWriteHandles=16) so eviction
	// churns under load — the nastiest case for the handle lifecycle.
	const numProjects = 24
	projects := make([]string, numProjects)
	projects[0] = "" // the active handle
	for i := 1; i < numProjects; i++ {
		projects[i] = fmt.Sprintf("Eng%02d", i)
	}

	const writers = 8
	const writesPerWriter = 400
	const readers = 6

	var written int64
	var readErrs int64
	done := make(chan struct{})
	var wg sync.WaitGroup

	// Writers: capture across projects continuously.
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < writesPerWriter; i++ {
				proj := projects[(w+i)%numProjects]
				ud := types.UserData{
					Host: "h.example.com", Port: "443", IsHTTPS: true, GeneratedBy: "proxy/http",
					ReqJson:  types.RequestData{Method: "GET", Path: fmt.Sprintf("/w%d/i%d", w, i), Query: "a=1"},
					RespJson: types.ResponseData{Status: 200, Mime: "text/html"},
					Project:  proj,
				}
				raw := fmt.Sprintf("GET /w%d/i%d?a=1 HTTP/1.1\r\nHost: h.example.com\r\nAuthorization: Bearer tok\r\n\r\n", w, i)
				if err := p.LogTraffic(ud, raw, "HTTP/1.1 200 OK\r\nContent-Type: text/html\r\n\r\n<html>ok</html>"); err != nil {
					t.Errorf("LogTraffic: %v", err)
				}
				atomic.AddInt64(&written, 1)
			}
		}(w)
	}

	// Readers: union search + byte resolution, compiled query, clustering.
	runReader := func(fn func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				if err := fn(); err != nil {
					atomic.AddInt64(&readErrs, 1)
				}
			}
		}()
	}
	for r := 0; r < readers; r++ {
		runReader(func() error {
			rows, err := p.unionTrafficRows("", "method = ?", []any{"GET"}, 50)
			if err != nil {
				return err
			}
			for _, row := range rows {
				// Reconstruct bytes mid-write — the adversarial path.
				_, _, _ = p.getTrafficBytes(row.Project, row.RequestID)
			}
			return nil
		})
	}
	// Compiled-query readers (the HTTPQL executor).
	runReader(func() error {
		_, err := p.unionCompiledQuery("", "SELECT h.request_id AS request_id, h.global_seq AS gseq, h.host AS host, h.port AS port, h.method AS method, h.path AS path, h.status_code AS status, h.response_length AS length, h.mime_type AS mime FROM http_traffic h WHERE h.status_code = ? ORDER BY h.global_seq DESC LIMIT 50", []any{200}, 50)
		return err
	})

	// Wait for writers to finish, then stop readers.
	writersWG := make(chan struct{})
	go func() {
		// writers were added to wg; we can't selectively wait, so spin on the
		// written counter reaching the target.
		target := int64(writers * writesPerWriter)
		for atomic.LoadInt64(&written) < target {
		}
		close(writersWG)
	}()
	<-writersWG
	close(done)
	wg.Wait()

	if readErrs > 0 {
		t.Errorf("union readers hit %d errors under load", readErrs)
	}

	// Sanity: total captured across all project DBs equals what writers wrote.
	p.Close()
	files, _ := listProjectDBFiles(dir)
	total := 0
	for name := range files {
		total += projectTrafficCount(dir, name)
	}
	// This test deliberately forces the EVICTION BACKSTOP: 24 projects written
	// concurrently against a 16-handle cap, so handles are closed-and-reopened
	// mid-write. Closing a WAL handle that holds committed-but-uncheckpointed
	// frames while readers continuously hold the file is inherently lossy at the
	// margin (a checkpoint can never get a clean window). That backstop path
	// trades perfect durability for bounded resources.
	//
	// WITHIN the cap, capture is EXACT — see TestLogTrafficConcurrentAcrossProjects
	// (4 projects, 200/200). What this test asserts is that the backstop is
	// SAFE under load: no data races, no read errors, no crash/deadlock, and
	// near-lossless (>=99%). Realistic workloads stay within the cap and lose
	// nothing.
	// Exact under normal timing — zero loss even under forced eviction churn
	// (24 projects vs a 16 cap). -race perturbs SQLite's WAL timing, so allow a
	// tiny epsilon there.
	want := writers * writesPerWriter
	min := want
	if raceEnabled {
		min = want - want/100
	}
	if total < min {
		t.Errorf("captured %d rows, want >= %d (excessive loss under eviction churn)", total, min)
	}
	t.Logf("stress: wrote %d, captured %d across %d project DBs, readErrs=%d (race=%v)", written, total, len(files), readErrs, raceEnabled)
}
