package app

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/campbellcharlie/lorg/internal/dadql/dadql"
)

// loadProxyFilters loads the filters for a specific proxy from the database
// Format: unique_id = "proxy/{proxyDBID}" in _ui collection
func (backend *Backend) loadProxyFilters(proxyDBID string) (string, error) {
	log.Printf("[FiltersManager] Loading filters for proxy %s from database...", proxyDBID)

	// Find the proxy filter record using unique_id = "proxy/{proxyDBID}"
	uniqueID := fmt.Sprintf("proxy/%s", proxyDBID)
	record, err := backend.DB.FindFirstRecord("_ui", "unique_id = ?", uniqueID)

	if err != nil {
		log.Printf("[FiltersManager] No filter record found for proxy %s, using empty filters: %v", proxyDBID, err)
		return "", nil
	}
	log.Printf("[FiltersManager] Found _ui filter record for proxy %s", proxyDBID)

	data := record.Get("data")
	if data == nil {
		log.Printf("[FiltersManager] No data field for proxy %s, using empty filters", proxyDBID)
		return "", nil
	}

	log.Printf("[FiltersManager][DEBUG] data type: %T", data)

	filterstring := ""

	// lorgdb auto-parses JSON strings into map[string]any
	if dataMap, ok := data.(map[string]any); ok {
		if fs, ok := dataMap["filterstring"].(string); ok {
			filterstring = fs
		} else {
			log.Printf("[FiltersManager] No filterstring in data (keys: %v), using empty filters", getMapKeys(dataMap))
		}
	} else if s, ok := data.(string); ok {
		// Fallback: raw JSON string not yet parsed
		var dataMap map[string]any
		if err := json.Unmarshal([]byte(s), &dataMap); err != nil {
			log.Printf("[FiltersManager][ERROR] Failed to unmarshal JSON string: %v", err)
			return "", err
		}
		if fs, ok := dataMap["filterstring"].(string); ok {
			filterstring = fs
		} else {
			log.Printf("[FiltersManager] No filterstring in data (keys: %v), using empty filters", getMapKeys(dataMap))
		}
	} else {
		log.Printf("[FiltersManager][ERROR] Unexpected data type: %T, using empty filters", data)
		return "", nil
	}

	log.Printf("[FiltersManager] Loaded filters for proxy %s: %s", proxyDBID, filterstring)
	return filterstring, nil
}

// Helper function to get map keys for debugging
func getMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func (rp *RawProxyWrapper) checkFilters(data map[string]any) bool {
	if rp.Filters == "" {
		return true
	}

	filter := rp.Filters
	filter = strings.ReplaceAll(filter, "req.", "req_json.")
	filter = strings.ReplaceAll(filter, "req_edited.", "req_edited_json.")
	filter = strings.ReplaceAll(filter, "resp.", "resp_json.")
	filter = strings.ReplaceAll(filter, "resp_edited.", "resp_edited_json.")

	log.Println("[Proxy.checkFilters] data: ", data)

	check, err := dadql.Filter(data, filter)
	if err != nil {
		log.Println("[Proxy.checkFilters] Filter parsing: ", filter, "Error: ", err)
		return false
	}

	log.Println("[Proxy.checkFilters] Filter parsing: ", filter, "\nResults: ", check)

	return check
}
