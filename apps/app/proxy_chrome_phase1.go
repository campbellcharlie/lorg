package app

// This file contains Phase 1 Chrome browser automation endpoints

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/campbellcharlie/lorg/lrx/browser"
	"github.com/labstack/echo/v4"
)

// ActivateTab endpoint - switches focus to a specific tab
func (backend *Backend) ActivateTab(e *echo.Echo) {
	e.POST("/api/proxy/chrome/tab/activate", func(c echo.Context) error {
		if err := requireAuth(c); err != nil {
			return err
		}

		type ActivateTabBody struct {
			ProxyID  string `json:"proxyId"`
			TargetID string `json:"targetId"`
		}

		var body ActivateTabBody
		if err := c.Bind(&body); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Invalid request body"})
		}

		if body.ProxyID == "" || body.TargetID == "" {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "proxyId and targetId are required"})
		}

		// Get proxy instance
		inst := ProxyMgr.GetInstance(body.ProxyID)
		if inst == nil {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Proxy not found"})
		}

		if inst.Browser != "chrome" {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Proxy does not have Chrome browser attached"})
		}

		// Get profile directory
		var profileDir string
		if inst.BrowserCmd != nil && len(inst.BrowserCmd.Args) > 0 {
			for _, arg := range inst.BrowserCmd.Args {
				if strings.HasPrefix(arg, "--user-data-dir=") {
					profileDir = strings.TrimPrefix(arg, "--user-data-dir=")
					break
				}
			}
		}

		if profileDir == "" {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Could not determine Chrome profile directory"})
		}

		// Get debug URL
		debugURL, err := browser.GetChromeDebugURL(profileDir)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": fmt.Sprintf("Failed to get Chrome debug URL: %v", err)})
		}

		// Activate tab
		if err := browser.ActivateTab(debugURL, body.TargetID); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": fmt.Sprintf("Failed to activate tab: %v", err)})
		}

		return c.JSON(http.StatusOK, map[string]interface{}{
			"ok":        true,
			"targetId":  body.TargetID,
			"timestamp": time.Now().Format(time.RFC3339),
		})
	})
}

// CloseTab endpoint - closes a specific tab
func (backend *Backend) CloseTab(e *echo.Echo) {
	e.POST("/api/proxy/chrome/tab/close", func(c echo.Context) error {
		if err := requireAuth(c); err != nil {
			return err
		}

		type CloseTabBody struct {
			ProxyID  string `json:"proxyId"`
			TargetID string `json:"targetId"`
		}

		var body CloseTabBody
		if err := c.Bind(&body); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Invalid request body"})
		}

		if body.ProxyID == "" || body.TargetID == "" {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "proxyId and targetId are required"})
		}

		// Get proxy instance
		inst := ProxyMgr.GetInstance(body.ProxyID)
		if inst == nil {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Proxy not found"})
		}

		if inst.Browser != "chrome" {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Proxy does not have Chrome browser attached"})
		}

		// Get profile directory
		var profileDir string
		if inst.BrowserCmd != nil && len(inst.BrowserCmd.Args) > 0 {
			for _, arg := range inst.BrowserCmd.Args {
				if strings.HasPrefix(arg, "--user-data-dir=") {
					profileDir = strings.TrimPrefix(arg, "--user-data-dir=")
					break
				}
			}
		}

		if profileDir == "" {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Could not determine Chrome profile directory"})
		}

		// Get debug URL
		debugURL, err := browser.GetChromeDebugURL(profileDir)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": fmt.Sprintf("Failed to get Chrome debug URL: %v", err)})
		}

		// Close tab
		if err := browser.CloseTab(debugURL, body.TargetID); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": fmt.Sprintf("Failed to close tab: %v", err)})
		}

		return c.JSON(http.StatusOK, map[string]interface{}{
			"ok":        true,
			"targetId":  body.TargetID,
			"timestamp": time.Now().Format(time.RFC3339),
		})
	})
}

// ReloadTab endpoint - reloads a specific tab
func (backend *Backend) ReloadTab(e *echo.Echo) {
	e.POST("/api/proxy/chrome/tab/reload", func(c echo.Context) error {
		if err := requireAuth(c); err != nil {
			return err
		}

		type ReloadTabBody struct {
			ProxyID     string `json:"proxyId"`
			TargetID    string `json:"targetId"`    // Optional, empty = active tab
			BypassCache bool   `json:"bypassCache"` // Optional
		}

		var body ReloadTabBody
		if err := c.Bind(&body); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Invalid request body"})
		}

		if body.ProxyID == "" {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "proxyId is required"})
		}

		// Get proxy instance
		inst := ProxyMgr.GetInstance(body.ProxyID)
		if inst == nil {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Proxy not found"})
		}

		if inst.Browser != "chrome" {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Proxy does not have Chrome browser attached"})
		}

		// Get profile directory
		var profileDir string
		if inst.BrowserCmd != nil && len(inst.BrowserCmd.Args) > 0 {
			for _, arg := range inst.BrowserCmd.Args {
				if strings.HasPrefix(arg, "--user-data-dir=") {
					profileDir = strings.TrimPrefix(arg, "--user-data-dir=")
					break
				}
			}
		}

		if profileDir == "" {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Could not determine Chrome profile directory"})
		}

		// Get debug URL
		debugURL, err := browser.GetChromeDebugURL(profileDir)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": fmt.Sprintf("Failed to get Chrome debug URL: %v", err)})
		}

		// Reload tab
		if err := browser.ReloadTab(debugURL, body.TargetID, body.BypassCache); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": fmt.Sprintf("Failed to reload tab: %v", err)})
		}

		return c.JSON(http.StatusOK, map[string]interface{}{
			"ok":        true,
			"targetId":  body.TargetID,
			"timestamp": time.Now().Format(time.RFC3339),
		})
	})
}

// GoBack endpoint - navigates back in browser history
func (backend *Backend) GoBack(e *echo.Echo) {
	e.POST("/api/proxy/chrome/tab/back", func(c echo.Context) error {
		if err := requireAuth(c); err != nil {
			return err
		}

		type GoBackBody struct {
			ProxyID  string `json:"proxyId"`
			TargetID string `json:"targetId"` // Optional, empty = active tab
		}

		var body GoBackBody
		if err := c.Bind(&body); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Invalid request body"})
		}

		if body.ProxyID == "" {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "proxyId is required"})
		}

		// Get proxy instance
		inst := ProxyMgr.GetInstance(body.ProxyID)
		if inst == nil {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Proxy not found"})
		}

		if inst.Browser != "chrome" {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Proxy does not have Chrome browser attached"})
		}

		// Get profile directory
		var profileDir string
		if inst.BrowserCmd != nil && len(inst.BrowserCmd.Args) > 0 {
			for _, arg := range inst.BrowserCmd.Args {
				if strings.HasPrefix(arg, "--user-data-dir=") {
					profileDir = strings.TrimPrefix(arg, "--user-data-dir=")
					break
				}
			}
		}

		if profileDir == "" {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Could not determine Chrome profile directory"})
		}

		// Get debug URL
		debugURL, err := browser.GetChromeDebugURL(profileDir)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": fmt.Sprintf("Failed to get Chrome debug URL: %v", err)})
		}

		// Go back
		if err := browser.GoBack(debugURL, body.TargetID); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": fmt.Sprintf("Failed to go back: %v", err)})
		}

		return c.JSON(http.StatusOK, map[string]interface{}{
			"ok":        true,
			"targetId":  body.TargetID,
			"timestamp": time.Now().Format(time.RFC3339),
		})
	})
}

// GoForward endpoint - navigates forward in browser history
func (backend *Backend) GoForward(e *echo.Echo) {
	e.POST("/api/proxy/chrome/tab/forward", func(c echo.Context) error {
		if err := requireAuth(c); err != nil {
			return err
		}

		type GoForwardBody struct {
			ProxyID  string `json:"proxyId"`
			TargetID string `json:"targetId"` // Optional, empty = active tab
		}

		var body GoForwardBody
		if err := c.Bind(&body); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Invalid request body"})
		}

		if body.ProxyID == "" {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "proxyId is required"})
		}

		// Get proxy instance
		inst := ProxyMgr.GetInstance(body.ProxyID)
		if inst == nil {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Proxy not found"})
		}

		if inst.Browser != "chrome" {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Proxy does not have Chrome browser attached"})
		}

		// Get profile directory
		var profileDir string
		if inst.BrowserCmd != nil && len(inst.BrowserCmd.Args) > 0 {
			for _, arg := range inst.BrowserCmd.Args {
				if strings.HasPrefix(arg, "--user-data-dir=") {
					profileDir = strings.TrimPrefix(arg, "--user-data-dir=")
					break
				}
			}
		}

		if profileDir == "" {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Could not determine Chrome profile directory"})
		}

		// Get debug URL
		debugURL, err := browser.GetChromeDebugURL(profileDir)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": fmt.Sprintf("Failed to get Chrome debug URL: %v", err)})
		}

		// Go forward
		if err := browser.GoForward(debugURL, body.TargetID); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": fmt.Sprintf("Failed to go forward: %v", err)})
		}

		return c.JSON(http.StatusOK, map[string]interface{}{
			"ok":        true,
			"targetId":  body.TargetID,
			"timestamp": time.Now().Format(time.RFC3339),
		})
	})
}
