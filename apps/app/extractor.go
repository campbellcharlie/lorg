package app

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/campbellcharlie/lorg/internal/lorgdb"
	"github.com/campbellcharlie/lorg/internal/utils"
	"github.com/labstack/echo/v4"
)

// ExtractData extracts specified fields from database records matching the host
// and saves them to a file. Returns the file path and any error.
func (backend *Backend) ExtractData(host string, fields []string, outputFile string) (string, error) {
	log.Printf("[ExtractData] Starting extraction for host: %s, fields: %v", host, fields)

	db := utils.ParseDatabaseName(host)

	log.Println("db: ", db)
	log.Println("host: ", host)
	log.Println("fields: ", fields)
	log.Println("outputFile: ", outputFile)

	records, err := backend.DB.FindRecords(db, "1=1")
	if err != nil {
		return "", fmt.Errorf("failed to find collection for host: %s: %w", host, err)
	}

	log.Printf("[ExtractData] Found %d records for host: %s", len(records), host)

	if len(records) == 0 {
		return "", fmt.Errorf("no records found for host: %s", host)
	}

	folder := path.Join(backend.Config.ProjectsDirectory, backend.Config.ProjectID, db)

	os.MkdirAll(folder, 0755)

	// Open file for writing
	file, err := os.Create(path.Join(folder, outputFile))
	if err != nil {
		return "", fmt.Errorf("failed to create output file: %w", err)
	}
	defer file.Close()

	// Extract and write data
	extractedCount := 0
	for _, record := range records {
		extracted := extractFieldsFromRecord(backend, record.Id, fields)

		if len(extracted) > 0 {
			file.Write([]byte(strings.Join(extracted, "\n")))
			file.WriteString("\n")
			extractedCount++
		}
	}

	fullPath := path.Join(folder, outputFile)
	log.Printf("[ExtractData] Extracted %d records to file: %s", extractedCount, fullPath)

	// Return absolute path
	absPath, err := filepath.Abs(fullPath)
	if err != nil {
		log.Printf("[ExtractData] Error getting absolute path: %v, returning original path", err)
		return fullPath, nil
	}

	return absPath, nil
}

// extractFieldsFromRecord extracts requested fields from a record and returns their values as strings
// Uses recordId to fetch records from _req, _resp, _req_edited, _resp_edited collections
func extractFieldsFromRecord(backend *Backend, recordId string, fields []string) []string {
	extracted := make([]string, 0)

	// Fetch related records using the same ID (they share the same ID)
	reqRecord, _ := backend.DB.FindRecordById("_req", recordId)
	respRecord, _ := backend.DB.FindRecordById("_resp", recordId)
	reqEditedRecord, _ := backend.DB.FindRecordById("_req_edited", recordId)
	respEditedRecord, _ := backend.DB.FindRecordById("_resp_edited", recordId)

	// Extract each requested field
	for _, field := range fields {
		value := extractFieldValue(reqRecord, respRecord, reqEditedRecord, respEditedRecord, field)
		if value != nil {
			// Convert value to string
			valueStr := convertValueToString(value)
			if valueStr != "" {
				extracted = append(extracted, valueStr)
			}
		}
	}

	return extracted
}

// convertValueToString converts a value to its string representation
func convertValueToString(value interface{}) string {
	if value == nil {
		return ""
	}

	switch v := value.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	case int, int8, int16, int32, int64:
		return fmt.Sprintf("%d", v)
	case uint, uint8, uint16, uint32, uint64:
		return fmt.Sprintf("%d", v)
	case float32, float64:
		return fmt.Sprintf("%g", v)
	case bool:
		return fmt.Sprintf("%t", v)
	case map[string]interface{}:
		// For JSON objects like headers, convert to JSON string
		jsonData, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(jsonData)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// extractFieldValue extracts a field value from records based on the field path
// Supports nested fields like "req.method", "req.url", "req.params", etc.
// Also supports req_edited.*, resp_edited.*, and req.raw, resp.raw
func extractFieldValue(reqRecord *lorgdb.Record, respRecord *lorgdb.Record, reqEditedRecord *lorgdb.Record, respEditedRecord *lorgdb.Record, field string) interface{} {
	// Handle req.* fields - use _req record
	if strings.HasPrefix(field, "req.") {
		if reqRecord == nil {
			return nil
		}

		subField := strings.TrimPrefix(field, "req.")

		// Handle params as alias for query
		if subField == "params" {
			subField = "query"
		}

		// Get field from _req record
		value := reqRecord.Get(subField)
		if value != nil {
			return value
		}

		// lorgdb auto-parses JSON columns into maps, so no special handling needed
		return nil
	}

	// Handle resp.* fields - use _resp record
	if strings.HasPrefix(field, "resp.") {
		if respRecord == nil {
			return nil
		}

		subField := strings.TrimPrefix(field, "resp.")

		// Get field from _resp record
		value := respRecord.Get(subField)
		if value != nil {
			return value
		}

		return nil
	}

	// Handle req_edited.* fields - use _req_edited record
	if strings.HasPrefix(field, "req_edited.") {
		if reqEditedRecord == nil {
			return nil
		}

		subField := strings.TrimPrefix(field, "req_edited.")

		// Handle params as alias for query
		if subField == "params" {
			subField = "query"
		}

		// Get field from _req_edited record
		value := reqEditedRecord.Get(subField)
		if value != nil {
			return value
		}

		return nil
	}

	// Handle resp_edited.* fields - use _resp_edited record
	if strings.HasPrefix(field, "resp_edited.") {
		if respEditedRecord == nil {
			return nil
		}

		subField := strings.TrimPrefix(field, "resp_edited.")

		// Get field from _resp_edited record
		value := respEditedRecord.Get(subField)
		if value != nil {
			return value
		}

		return nil
	}

	// Unknown field - return nil (only req.*, resp.*, req_edited.*, resp_edited.* are supported)
	return nil
}

// ExtractDataEndpoint creates an API endpoint for data extraction
func (backend *Backend) ExtractDataEndpoint(e *echo.Echo) {
	e.POST("/api/extract", func(c echo.Context) error {
		if err := requireAuth(c); err != nil {
			return err
		}

		var data map[string]interface{}
		if err := c.Bind(&data); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"error": "Invalid request body",
			})
		}

		// Extract required fields
		host, ok := data["host"].(string)
		if !ok || host == "" {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"error": "host is required",
			})
		}

		// Get fields to extract
		var fields []string
		if fieldsInterface, ok := data["fields"].([]interface{}); ok {
			for _, f := range fieldsInterface {
				if fieldStr, ok := f.(string); ok {
					fields = append(fields, fieldStr)
				}
			}
		} else if fieldsStr, ok := data["fields"].(string); ok {
			// Support comma-separated string
			fields = strings.Split(fieldsStr, ",")
			for i := range fields {
				fields[i] = strings.TrimSpace(fields[i])
			}
		}

		if len(fields) == 0 {
			// Default fields if none specified
			fields = []string{"req.method", "req.url", "req.path", "req.params"}
		}

		// Get output file path (optional, defaults to cache directory)
		outputFile, ok := data["outputFile"].(string)
		if !ok || outputFile == "" {
			// Generate default filename based on host and timestamp
			timestamp := time.Now().Format("20060102_150405")
			safeHost := strings.ReplaceAll(strings.ReplaceAll(host, "://", "_"), "/", "_")
			outputFile = filepath.Join(backend.Config.CacheDirectory, fmt.Sprintf("extract_%s_%s.jsonl", safeHost, timestamp))
		}

		// Perform extraction
		filePath, err := backend.ExtractData(host, fields, outputFile)
		if err != nil {
			log.Printf("[ExtractDataEndpoint] Error: %v", err)
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"error":   "Failed to extract data",
				"message": err.Error(),
			})
		}

		return c.JSON(http.StatusOK, map[string]interface{}{
			"success":     true,
			"filePath":    filePath,
			"host":        host,
			"fields":      fields,
			"extractedAt": time.Now().Format(time.RFC3339),
		})
	})
}
