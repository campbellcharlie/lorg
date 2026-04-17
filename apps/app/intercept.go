package app

import (
	"fmt"
	"log"
	"net/http"
	"sync"

	"github.com/labstack/echo/v4"
)

// InterceptUpdateChannels stores channels for each intercept waiting goroutine
var (
	interceptChannels   = make(map[string]chan InterceptUpdate)
	interceptChannelsMu sync.RWMutex
)

// RegisterInterceptChannel registers a channel for a specific intercept ID
func RegisterInterceptChannel(id string, ch chan InterceptUpdate) {
	interceptChannelsMu.Lock()
	defer interceptChannelsMu.Unlock()
	interceptChannels[id] = ch
}

// UnregisterInterceptChannel removes the channel for a specific intercept ID
func UnregisterInterceptChannel(id string) {
	interceptChannelsMu.Lock()
	defer interceptChannelsMu.Unlock()
	if ch, exists := interceptChannels[id]; exists {
		close(ch)
		delete(interceptChannels, id)
	}
}

// NotifyInterceptUpdate sends an update to the waiting goroutine
func NotifyInterceptUpdate(id string, update InterceptUpdate) {
	interceptChannelsMu.RLock()
	defer interceptChannelsMu.RUnlock()
	if ch, exists := interceptChannels[id]; exists {
		select {
		case ch <- update:
			log.Printf("[InterceptManager] Notified waiting goroutine for ID=%s", id)
		default:
			log.Printf("[InterceptManager][WARN] Channel for ID=%s is not ready", id)
		}
	}
}

// forwardAllIntercepts forwards all pending intercept requests when intercept is disabled
func (backend *Backend) forwardAllIntercepts() {
	interceptChannelsMu.RLock()
	defer interceptChannelsMu.RUnlock()

	if len(interceptChannels) == 0 {
		log.Println("[InterceptManager] No pending intercepts to forward")
		return
	}

	log.Printf("[InterceptManager] Forwarding %d pending intercepts via channels", len(interceptChannels))

	// Directly notify all waiting goroutines via their channels
	// Each goroutine will handle deleting its own record
	forwardUpdate := InterceptUpdate{
		Action:        "forward",
		IsReqEdited:   false,
		IsRespEdited:  false,
		ReqEditedRaw:  "",
		RespEditedRaw: "",
	}

	for id, ch := range interceptChannels {
		select {
		case ch <- forwardUpdate:
			log.Printf("[InterceptManager] Forwarded intercept %s via channel", id)
		default:
			log.Printf("[InterceptManager][WARN] Channel for ID=%s is not ready", id)
		}
	}

	log.Println("[InterceptManager] All pending intercepts forwarded via channels")
}

// forwardProxyIntercepts forwards all pending intercept requests for a specific proxy
func (backend *Backend) forwardProxyIntercepts(proxyDBID string) {
	interceptChannelsMu.RLock()
	defer interceptChannelsMu.RUnlock()

	if len(interceptChannels) == 0 {
		log.Printf("[InterceptManager] No pending intercepts to forward for proxy %s", proxyDBID)
		return
	}

	log.Printf("[InterceptManager] Forwarding pending intercepts for proxy %s", proxyDBID)

	// TODO: We need to track which intercepts belong to which proxy
	// For now, query the lorgdb database to check each intercept
	forwardUpdate := InterceptUpdate{
		Action:        "forward",
		IsReqEdited:   false,
		IsRespEdited:  false,
		ReqEditedRaw:  "",
		RespEditedRaw: "",
	}

	forwardedCount := 0
	expectedGeneratedBy := fmt.Sprintf("proxy/%s", proxyDBID)

	for id, ch := range interceptChannels {
		// Check if this intercept belongs to the proxy
		interceptRecord, err := backend.DB.FindRecordById("_intercept", id)
		if err != nil {
			log.Printf("[InterceptManager][WARN] Failed to find intercept record %s: %v", id, err)
			continue
		}

		// generated_by is stored directly on the intercept record
		recordGeneratedBy := interceptRecord.GetString("generated_by")
		if recordGeneratedBy == expectedGeneratedBy {
			select {
			case ch <- forwardUpdate:
				log.Printf("[InterceptManager] Forwarded intercept %s for proxy %s", id, proxyDBID)
				forwardedCount++
			default:
				log.Printf("[InterceptManager][WARN] Channel for ID=%s is not ready", id)
			}
		}
	}

	log.Printf("[InterceptManager] Forwarded %d intercepts for proxy %s", forwardedCount, proxyDBID)
}

// UpdateInterceptFilters updates the intercept filters for all proxies
func (backend *Backend) UpdateInterceptFilters(filters string) {
	// Apply to all proxies
	ProxyMgr.ApplyToAllProxies(func(proxy *RawProxyWrapper, proxyID string) {
		proxy.Filters = filters
	})
	log.Printf("[InterceptManager] Updated intercept filters: %s", filters)
}

// InterceptActionRequest represents the request body for intercept actions
type InterceptActionRequest struct {
	ID           string `json:"id"`
	Action       string `json:"action"` // "forward" or "drop"
	IsReqEdited  bool   `json:"is_req_edited,omitempty"`
	IsRespEdited bool   `json:"is_resp_edited,omitempty"`
	ReqEdited    string `json:"req_edited,omitempty"`  // Raw HTTP request string
	RespEdited   string `json:"resp_edited,omitempty"` // Raw HTTP response string
}

// InterceptEndpoints registers the HTTP endpoints for intercept management
func (backend *Backend) InterceptEndpoints(e *echo.Echo) {
	// POST /api/intercept/action - Handle intercept actions (forward/drop)
	e.POST("/api/intercept/action", func(c echo.Context) error {
		if err := requireAuth(c); err != nil {
			return err
		}

		var req InterceptActionRequest
		if err := c.Bind(&req); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"error": "Invalid request body",
			})
		}

		// Validate action
		if req.Action != "forward" && req.Action != "drop" {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"error": "Invalid action. Must be 'forward' or 'drop'",
			})
		}

		// Validate ID
		if req.ID == "" {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"error": "Intercept ID is required",
			})
		}

		log.Printf("[InterceptAPI] Received action request: ID=%s, Action=%s", req.ID, req.Action)

		log.Printf("[InterceptAPI] Successfully updated intercept: ID=%s, Action=%s", req.ID, req.Action)

		// Directly notify the waiting goroutine via channel with the raw edited strings
		update := InterceptUpdate{
			Action:        req.Action,
			IsReqEdited:   req.IsReqEdited,
			IsRespEdited:  req.IsRespEdited,
			ReqEditedRaw:  req.ReqEdited,
			RespEditedRaw: req.RespEdited,
		}
		NotifyInterceptUpdate(req.ID, update)
		log.Printf("[InterceptAPI] Notified waiting goroutine for ID=%s (req_edited=%v, resp_edited=%v)",
			req.ID, req.IsReqEdited, req.IsRespEdited)

		return c.JSON(http.StatusOK, map[string]interface{}{
			"success": true,
			"message": "Intercept action processed successfully",
		})
	})

	log.Println("[InterceptAPI] Intercept endpoints registered successfully")
}
