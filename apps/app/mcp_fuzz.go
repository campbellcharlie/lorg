package app

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/campbellcharlie/lorg/lrx/fuzzer"
	"github.com/mark3labs/mcp-go/mcp"
)

// ---------------------------------------------------------------------------
// Fuzzer state
// ---------------------------------------------------------------------------

type fuzzState struct {
	mu        sync.Mutex
	config    *fuzzer.FuzzerConfig
	fuzzer    *fuzzer.Fuzzer
	running   bool
	results   []fuzzResult
	startTime time.Time
}

type fuzzResult struct {
	Index    int               `json:"index"`
	Request  string            `json:"request"`
	Response string            `json:"response"`
	TimeMs   float64           `json:"timeMs"`
	Error    string            `json:"error,omitempty"`
	Markers  map[string]string `json:"markers,omitempty"`
}

// activeFuzz is the package-level fuzzer state.
// Thread-safe: fuzzState uses an internal mutex for all field access.
var activeFuzz = &fuzzState{}

// ---------------------------------------------------------------------------
// Input schema
// ---------------------------------------------------------------------------

type FuzzArgs struct {
	Action      string              `json:"action" jsonschema:"required,enum=configure,enum=start,enum=stop,enum=status,enum=results" jsonschema_description:"configure: set up fuzzer; start: begin fuzzing; stop: halt; status: progress; results: get findings"`
	Host        string              `json:"host,omitempty" jsonschema_description:"Target host"`
	Port        int                 `json:"port,omitempty" jsonschema_description:"Target port (default 80, or 443 with TLS)"`
	TLS         bool                `json:"tls,omitempty" jsonschema_description:"Use TLS"`
	HTTP2       bool                `json:"http2,omitempty" jsonschema_description:"Use HTTP/2"`
	Request     string              `json:"request,omitempty" jsonschema_description:"Raw HTTP request template with §marker§ placeholders"`
	Markers     map[string][]string `json:"markers,omitempty" jsonschema_description:"Map of marker placeholder to payload list, e.g. {\"§user§\": [\"admin\", \"test\"], \"§pass§\": [\"123\", \"456\"]}"`
	Mode        string              `json:"mode,omitempty" jsonschema_description:"Fuzzing mode: cluster_bomb (default, all combinations) or pitch_fork (parallel iteration)"`
	Concurrency int                 `json:"concurrency,omitempty" jsonschema_description:"Number of concurrent workers (default 40)"`
	Timeout     int                 `json:"timeout,omitempty" jsonschema_description:"Request timeout in seconds (default 10)"`
	Limit       int                 `json:"limit,omitempty" jsonschema_description:"Max results to return for results action (default 100)"`
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

func (backend *Backend) fuzzHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args FuzzArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	switch args.Action {
	case "configure":
		return backend.fuzzConfigureHandler(args)
	case "start":
		return backend.fuzzStartHandler()
	case "stop":
		return backend.fuzzStopHandler()
	case "status":
		return backend.fuzzStatusHandler()
	case "results":
		return backend.fuzzResultsHandler(args)
	default:
		return mcp.NewToolResultError("unknown action: " + args.Action + ". Valid: configure, start, stop, status, results"), nil
	}
}

func (backend *Backend) fuzzConfigureHandler(args FuzzArgs) (*mcp.CallToolResult, error) {
	if args.Host == "" {
		return mcp.NewToolResultError("host is required"), nil
	}
	if args.Request == "" {
		return mcp.NewToolResultError("request template is required"), nil
	}
	if len(args.Markers) == 0 {
		return mcp.NewToolResultError("at least one marker with payloads is required"), nil
	}

	port := args.Port
	if port == 0 {
		if args.TLS {
			port = 443
		} else {
			port = 80
		}
	}
	portStr := fmt.Sprintf("%d", port)

	concurrency := args.Concurrency
	if concurrency <= 0 {
		concurrency = 40
	}
	if concurrency > 100 {
		concurrency = 100
	}

	mode := args.Mode
	if mode == "" {
		mode = fuzzer.ModeClusterBomb
	}

	timeout := time.Duration(args.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	// Convert map[string][]string to map[string]any for the fuzzer config.
	// The fuzzer accepts map[string]any where values are []string (inline) or string (file path).
	markers := make(map[string]any, len(args.Markers))
	for name, payloads := range args.Markers {
		markers[name] = payloads
	}

	config := &fuzzer.FuzzerConfig{
		Host:        args.Host,
		Port:        portStr,
		UseTLS:      args.TLS,
		UseHTTP2:    args.HTTP2,
		Request:     args.Request,
		Markers:     markers,
		Mode:        mode,
		Concurrency: concurrency,
		Timeout:     timeout,
	}

	// Calculate total requests for the summary
	total := 0
	if mode == fuzzer.ModePitchFork {
		min := -1
		for _, payloads := range args.Markers {
			if min == -1 || len(payloads) < min {
				min = len(payloads)
			}
		}
		if min > 0 {
			total = min
		}
	} else {
		total = 1
		for _, payloads := range args.Markers {
			total *= len(payloads)
		}
	}

	activeFuzz.mu.Lock()
	activeFuzz.config = config
	activeFuzz.results = nil
	activeFuzz.fuzzer = nil
	activeFuzz.mu.Unlock()

	return mcpJSONResult(map[string]any{
		"success":       true,
		"host":          args.Host,
		"port":          port,
		"tls":           args.TLS,
		"http2":         args.HTTP2,
		"mode":          mode,
		"markerCount":   len(markers),
		"totalRequests": total,
		"concurrency":   concurrency,
		"timeout":       timeout.String(),
	})
}

func (backend *Backend) fuzzStartHandler() (*mcp.CallToolResult, error) {
	activeFuzz.mu.Lock()
	if activeFuzz.running {
		activeFuzz.mu.Unlock()
		return mcp.NewToolResultError("fuzzer already running; use stop first"), nil
	}
	if activeFuzz.config == nil {
		activeFuzz.mu.Unlock()
		return mcp.NewToolResultError("fuzzer not configured; use configure first"), nil
	}

	config := *activeFuzz.config // copy
	activeFuzz.results = nil
	activeFuzz.running = true
	activeFuzz.startTime = time.Now()

	f := fuzzer.NewFuzzer(config)
	activeFuzz.fuzzer = f
	activeFuzz.mu.Unlock()

	// Run fuzzer in background goroutine, collect results from f.Results channel.
	go func() {
		// Start the fuzz loop in another goroutine; Fuzz() blocks until done then closes f.Results.
		go func() {
			if err := f.Fuzz(); err != nil {
				log.Printf("[MCP fuzz] Fuzz() error: %v", err)
			}
		}()

		idx := 0
		for raw := range f.Results {
			r, ok := raw.(fuzzer.FuzzerResult)
			if !ok {
				log.Printf("[MCP fuzz] unexpected result type: %T", raw)
				continue
			}

			result := fuzzResult{
				Index:    idx,
				Request:  r.Request,
				Response: r.Response,
				TimeMs:   float64(r.Time.Milliseconds()),
				Error:    r.Error,
			}
			if r.Markers != nil {
				result.Markers = r.Markers
			}

			activeFuzz.mu.Lock()
			activeFuzz.results = append(activeFuzz.results, result)
			activeFuzz.mu.Unlock()
			idx++
		}

		activeFuzz.mu.Lock()
		activeFuzz.running = false
		activeFuzz.mu.Unlock()
		log.Printf("[MCP fuzz] Fuzzing complete: %d results collected", idx)
	}()

	return mcpJSONResult(map[string]any{
		"success": true,
		"message": "Fuzzer started in background. Use status to monitor progress, results to retrieve findings.",
	})
}

func (backend *Backend) fuzzStopHandler() (*mcp.CallToolResult, error) {
	activeFuzz.mu.Lock()
	defer activeFuzz.mu.Unlock()

	if !activeFuzz.running {
		return mcp.NewToolResultError("fuzzer not running"), nil
	}

	if activeFuzz.fuzzer != nil {
		activeFuzz.fuzzer.Stop()
	}
	activeFuzz.running = false

	return mcpJSONResult(map[string]any{
		"success":      true,
		"resultsSoFar": len(activeFuzz.results),
	})
}

func (backend *Backend) fuzzStatusHandler() (*mcp.CallToolResult, error) {
	activeFuzz.mu.Lock()
	defer activeFuzz.mu.Unlock()

	status := map[string]any{
		"configured": activeFuzz.config != nil,
		"running":    activeFuzz.running,
		"results":    len(activeFuzz.results),
	}

	if activeFuzz.running {
		status["elapsed"] = time.Since(activeFuzz.startTime).String()
	}

	if activeFuzz.fuzzer != nil {
		completed, total := activeFuzz.fuzzer.GetProgress()
		status["completed"] = completed
		status["total"] = total
		if total > 0 {
			status["percent"] = fmt.Sprintf("%.1f%%", float64(completed)/float64(total)*100)
		}
	}

	return mcpJSONResult(status)
}

func (backend *Backend) fuzzResultsHandler(args FuzzArgs) (*mcp.CallToolResult, error) {
	activeFuzz.mu.Lock()
	defer activeFuzz.mu.Unlock()

	limit := args.Limit
	if limit <= 0 {
		limit = 100
	}

	results := activeFuzz.results
	truncated := false
	if len(results) > limit {
		results = results[len(results)-limit:]
		truncated = true
	}

	return mcpJSONResult(map[string]any{
		"results":   results,
		"count":     len(results),
		"total":     len(activeFuzz.results),
		"truncated": truncated,
		"running":   activeFuzz.running,
	})
}
