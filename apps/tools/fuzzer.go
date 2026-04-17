package tools

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/campbellcharlie/lorg/internal/lorgdb"
	"github.com/campbellcharlie/lorg/internal/sdk"
	"github.com/campbellcharlie/lorg/lrx/fuzzer"
	"github.com/campbellcharlie/lorg/lrx/rawhttp"
	"github.com/labstack/echo/v4"
)

type FuzzerManager struct {
	instances map[string]*fuzzer.Fuzzer
	mu        sync.RWMutex
}

var FuzzerMgr = &FuzzerManager{
	instances: make(map[string]*fuzzer.Fuzzer),
}

type FuzzerStartRequest struct {
	Collection  string         `json:"collection"`
	Request     string         `json:"request"`
	Host        string         `json:"host"`
	Port        string         `json:"port"`
	UseTLS      bool           `json:"useTLS"`
	UseHTTP2    bool           `json:"http2"`   // Enable HTTP/2 support
	Markers     map[string]any `json:"markers"` // marker -> string (file path) or []string (inline payloads)
	Mode        string         `json:"mode"`
	Concurrency int            `json:"concurrency"`
	Timeout     float64        `json:"timeout"` // in seconds
	ProcessData any            `json:"process_data"`
	GeneratedBy string         `json:"generated_by"`
}

// CreateFuzzerTable creates a table for storing fuzzer results.
// Replaces the old PocketBase CreateCollection(name, schemas.Fuzzer) pattern.
func (backend *Tools) CreateFuzzerTable(tableName string) error {
	ddl := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS "%s" (
		id               TEXT PRIMARY KEY NOT NULL,
		created          TEXT NOT NULL DEFAULT '',
		updated          TEXT NOT NULL DEFAULT '',
		fuzzer_id        TEXT NOT NULL DEFAULT '',
		raw_request      TEXT NOT NULL DEFAULT '',
		raw_response     TEXT NOT NULL DEFAULT '',
		req_method       TEXT NOT NULL DEFAULT '',
		req_url          TEXT NOT NULL DEFAULT '',
		req_version      TEXT NOT NULL DEFAULT '',
		req_headers      JSON DEFAULT NULL,
		resp_version     TEXT NOT NULL DEFAULT '',
		resp_status      REAL NOT NULL DEFAULT 0,
		resp_status_full TEXT NOT NULL DEFAULT '',
		resp_length      REAL NOT NULL DEFAULT 0,
		resp_headers     JSON DEFAULT NULL,
		time             REAL NOT NULL DEFAULT 0,
		markers          JSON DEFAULT NULL
	)`, tableName)
	_, err := backend.DB.Exec(ddl)
	return err
}

// parseAndSaveResult parses the request and response using rawhttp and returns data for saving
func parseAndSaveResult(rawRequest, rawResponse string) map[string]any {
	data := make(map[string]any)

	// Save raw request and response
	data["raw_request"] = rawRequest
	data["raw_response"] = rawResponse

	// Parse request
	parsedReq := rawhttp.ParseRequest([]byte(rawRequest))
	data["req_method"] = parsedReq.Method
	data["req_url"] = parsedReq.URL
	data["req_version"] = parsedReq.HTTPVersion

	// Convert headers to JSON
	if len(parsedReq.Headers) > 0 {
		reqHeadersJSON, err := json.Marshal(parsedReq.Headers)
		if err == nil {
			var headers interface{}
			if err := json.Unmarshal(reqHeadersJSON, &headers); err == nil {
				data["req_headers"] = headers
			}
		}
	}

	// Parse response
	parsedResp := rawhttp.ParseResponse([]byte(rawResponse))
	data["resp_version"] = parsedResp.Version
	data["resp_status"] = parsedResp.Status
	data["resp_status_full"] = parsedResp.StatusFull
	data["resp_length"] = len(rawResponse)

	// Convert headers to JSON
	if len(parsedResp.Headers) > 0 {
		respHeadersJSON, err := json.Marshal(parsedResp.Headers)
		if err == nil {
			var headers interface{}
			if err := json.Unmarshal(respHeadersJSON, &headers); err == nil {
				data["resp_headers"] = headers
			}
		}
	}

	return data
}

func (backend *Tools) StartFuzzer(e *echo.Echo) {
	e.POST("/api/fuzzer/start", func(c echo.Context) error {
		if err := requireLocalhost(c); err != nil {
			return err
		}

		var body FuzzerStartRequest
		if err := c.Bind(&body); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"status":     "error",
				"process_id": "",
				"fuzzer_id":  "",
				"error":      err.Error(),
			})
		}

		// Validate required fields
		if body.Request == "" {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"status":     "error",
				"process_id": "",
				"fuzzer_id":  "",
				"error":      "request is required",
			})
		}
		if body.Host == "" {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"status":     "error",
				"process_id": "",
				"fuzzer_id":  "",
				"error":      "host is required",
			})
		}
		if len(body.Markers) == 0 {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"status":     "error",
				"process_id": "",
				"fuzzer_id":  "",
				"error":      "markers is required",
			})
		}
		for key, value := range body.Markers {
			switch v := value.(type) {
			case string:
				if v == "" {
					return c.JSON(http.StatusBadRequest, map[string]interface{}{
						"status":     "error",
						"process_id": "",
						"fuzzer_id":  "",
						"error":      fmt.Sprintf("marker '%s' must have a file path", key),
					})
				}
			case []interface{}:
				if len(v) == 0 {
					return c.JSON(http.StatusBadRequest, map[string]interface{}{
						"status":     "error",
						"process_id": "",
						"fuzzer_id":  "",
						"error":      fmt.Sprintf("marker '%s' must have at least one payload", key),
					})
				}
			default:
				return c.JSON(http.StatusBadRequest, map[string]interface{}{
					"status":     "error",
					"process_id": "",
					"fuzzer_id":  "",
					"error":      fmt.Sprintf("marker '%s' must be a string (file path) or array (payloads)", key),
				})
			}
		}

		// Clean host (remove http:// or https://)
		host := strings.TrimPrefix(body.Host, "http://")
		host = strings.TrimPrefix(host, "https://")

		// Convert timeout from seconds to duration
		timeout := time.Duration(body.Timeout) * time.Second
		if timeout == 0 {
			timeout = 10 * time.Second
		}

		// Create process in main app's database using SDK
		if backend.AppSDK == nil {
			return c.JSON(http.StatusServiceUnavailable, map[string]interface{}{
				"status":     "error",
				"process_id": "",
				"fuzzer_id":  "",
				"error":      "Not connected to main app. Please initialize SDK using tools.LoginSDK(url, email, password)",
			})
		}

		// Create fuzzer config
		config := fuzzer.FuzzerConfig{
			Request:     body.Request,
			Host:        host,
			Port:        body.Port,
			UseTLS:      body.UseTLS,
			UseHTTP2:    body.UseHTTP2,
			Markers:     body.Markers,
			Mode:        body.Mode,
			Concurrency: body.Concurrency,
			Timeout:     timeout,
		}

		id, err := backend.AppSDK.CreateProcess(sdk.CreateProcessRequest{
			Name:        "Fuzzer",
			Description: fmt.Sprintf("Fuzzing %s", body.Host),
			Type:        "fuzzer",
			State:       "In Queue",
			Data: map[string]any{
				"request_body": body,
			},
			GeneratedBy: body.GeneratedBy,
		})
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"status":     "error",
				"process_id": "",
				"fuzzer_id":  "",
				"error":      fmt.Sprintf("Failed to create process: %v", err),
			})
		}

		// Create table for this fuzzer's results
		err = backend.CreateFuzzerTable(body.Collection)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"status":     "error",
				"process_id": "",
				"fuzzer_id":  "",
				"error":      fmt.Sprintf("Failed to create collection: %v", err),
			})
		}

		// Create fuzzer instance
		f := fuzzer.NewFuzzer(config)

		// Store fuzzer instance
		FuzzerMgr.mu.Lock()
		FuzzerMgr.instances[id] = f
		FuzzerMgr.mu.Unlock()

		// Update state to running via SDK
		err = backend.AppSDK.UpdateProcess(id, sdk.ProgressUpdate{
			Completed: 0,
			Total:     100,
			Message:   "Starting fuzzer...",
			State:     "Running",
		})
		if err != nil {
			log.Printf("[StartFuzzer] Failed to update process state: %v", err)
		}

		// Capture collection name and DB for goroutine use
		collectionName := body.Collection
		db := backend.DB

		// Start result processing in a goroutine
		go func() {
			const batchSize = 100
			const flushInterval = 2 * time.Second
			const progressUpdateInterval = 1 * time.Second
			batch := make([]*lorgdb.Record, 0, batchSize)
			ticker := time.NewTicker(flushInterval)
			progressTicker := time.NewTicker(progressUpdateInterval)
			defer ticker.Stop()
			defer progressTicker.Stop()

			flush := func() {
				if len(batch) == 0 {
					return
				}
				err := db.RunInTransaction(func(tx *lorgdb.LorgTx) error {
					for _, record := range batch {
						if err := tx.SaveRecord(record); err != nil {
							return err
						}
					}
					return nil
				})
				if err != nil {
					log.Printf("[StartFuzzer] Failed to save batch for %s: %v", id, err)
				}
				batch = batch[:0]
			}

			for {
				select {
				case result, ok := <-f.Results:
					if !ok {
						flush()
						goto resultsDone
					}
					fuzzerResult, ok := result.(fuzzer.FuzzerResult)
					if !ok {
						log.Printf("[StartFuzzer] Invalid result type: %T", result)
						continue
					}

					// Parse request and response
					data := parseAndSaveResult(fuzzerResult.Request, fuzzerResult.Response)

					// Add common fields
					data["fuzzer_id"] = id
					data["time"] = fuzzerResult.Time.Nanoseconds()
					data["markers"] = fuzzerResult.Markers

					// Create record
					record := lorgdb.NewRecord(collectionName)
					for key, value := range data {
						record.Set(key, value)
					}
					batch = append(batch, record)

					if len(batch) >= batchSize {
						flush()
					}
				case <-ticker.C:
					flush()
				case <-progressTicker.C:
					// Update progress via SDK
					completed, total := f.GetProgress()
					if total > 0 {
						err := backend.AppSDK.UpdateProcess(id, sdk.ProgressUpdate{
							Completed: completed,
							Total:     total,
							Message:   fmt.Sprintf("Processing: %d/%d requests", completed, total),
							State:     "Running",
						})
						if err != nil {
							log.Printf("[StartFuzzer] Failed to update progress: %v", err)
						}
					}
				}
			}
		resultsDone:

			log.Println("[StartFuzzer] results processing completed for ", id)

			// Final progress update via SDK
			completed, total := f.GetProgress()
			err := backend.AppSDK.CompleteProcess(id, fmt.Sprintf("Completed: %d/%d requests", completed, total))
			if err != nil {
				log.Printf("[StartFuzzer] Failed to complete process: %v", err)
			}

			// Clean up after all results are processed
			FuzzerMgr.mu.Lock()
			delete(FuzzerMgr.instances, id)
			FuzzerMgr.mu.Unlock()
		}()

		// Start fuzzing in a separate goroutine (non-blocking)
		go func() {
			err := f.Fuzz()
			if err != nil {
				log.Printf("[StartFuzzer] Fuzzer error for %s: %v", id, err)

				// Update process as failed via SDK
				sdkErr := backend.AppSDK.FailProcess(id, fmt.Sprintf("Fuzzer error: %v", err))
				if sdkErr != nil {
					log.Printf("[StartFuzzer] Failed to update process as failed: %v", sdkErr)
				}

				// Clean up
				FuzzerMgr.mu.Lock()
				delete(FuzzerMgr.instances, id)
				FuzzerMgr.mu.Unlock()
			}
		}()

		// Return immediately with the fuzzer ID
		return c.JSON(http.StatusOK, map[string]interface{}{
			"status":     "started",
			"process_id": id,
			"fuzzer_id":  id,
		})
	})
}

func (backend *Tools) StopFuzzer(e *echo.Echo) {
	e.POST("/api/fuzzer/stop", func(c echo.Context) error {
		if err := requireLocalhost(c); err != nil {
			return err
		}

		var body map[string]string
		if err := c.Bind(&body); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"status":     "error",
				"process_id": "",
				"fuzzer_id":  "",
				"error":      err.Error(),
			})
		}

		id := body["id"]
		if id == "" {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"status":     "error",
				"process_id": "",
				"fuzzer_id":  "",
				"error":      "id is required",
			})
		}

		FuzzerMgr.mu.RLock()
		f, exists := FuzzerMgr.instances[id]
		FuzzerMgr.mu.RUnlock()

		if !exists {
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"status":     "error",
				"process_id": id,
				"fuzzer_id":  id,
				"error":      "fuzzer not found",
			})
		}

		// Get current progress before stopping
		completed, total := f.GetProgress()

		// Stop the fuzzer
		f.Stop()

		// Update process with final progress via SDK
		err := backend.AppSDK.UpdateProcess(id, sdk.ProgressUpdate{
			Completed: completed,
			Total:     total,
			Message:   fmt.Sprintf("Stopped by user at %d/%d requests", completed, total),
			State:     "Killed",
		})
		if err != nil {
			log.Printf("[StopFuzzer] Failed to update process: %v", err)
		}

		return c.JSON(http.StatusOK, map[string]interface{}{
			"status":     "stopped",
			"process_id": id,
			"fuzzer_id":  id,
		})
	})
}
