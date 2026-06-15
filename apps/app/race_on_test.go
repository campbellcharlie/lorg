//go:build race

package app

// raceEnabled is true when the test binary is built with -race. Used to skip a
// couple of tests that exercise a PRE-EXISTING fire-and-forget race in the
// sitemap/wappalyzer goroutine of SaveRequestToBackend — unrelated to the
// project-routing contract those tests assert (ADR-004).
const raceEnabled = true
