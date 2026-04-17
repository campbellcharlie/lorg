package tools

import (
	"fmt"
	"log"
	"net/http"

	"github.com/campbellcharlie/lorg/internal/sdk"
	"github.com/labstack/echo/v4"
)

// LoginSDK initializes and authenticates the SDK client for connecting to the main app
func (t *Tools) LoginSDK(url, email, password string) error {
	if url == "" {
		return fmt.Errorf("app URL cannot be empty")
	}
	if email == "" {
		return fmt.Errorf("admin email cannot be empty")
	}
	if password == "" {
		return fmt.Errorf("admin password cannot be empty")
	}

	// Store the URL
	t.AppURL = url

	// Create SDK client
	t.AppSDK = sdk.NewClient(
		url,
		sdk.WithAdminEmailPassword(email, password),
	)

	// Test the connection
	if err := t.AppSDK.Authorize(); err != nil {
		return fmt.Errorf("failed to authenticate with main app: %w", err)
	}

	log.Printf("[LoginSDK] Successfully connected to main app at %s", url)
	return nil
}

type LoginSDKRequest struct {
	URL      string `json:"url"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (backend *Tools) SDKStatus(e *echo.Echo) {
	e.GET("/api/sdk/status", func(c echo.Context) error {
		if err := requireLocalhost(c); err != nil {
			return err
		}

		return c.JSON(http.StatusOK, map[string]interface{}{
			"status":    "success",
			"connected": backend.AppSDK != nil,
			"url":       backend.AppURL,
		})
	})
}

func (backend *Tools) LoginSDKEndpoint(e *echo.Echo) {
	e.POST("/api/sdk/login", func(c echo.Context) error {
		if err := requireLocalhost(c); err != nil {
			return err
		}

		var body LoginSDKRequest
		if err := c.Bind(&body); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"status":    "error",
				"connected": false,
				"error":     err.Error(),
			})
		}

		// Validate required fields
		if body.URL == "" {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"status":    "error",
				"connected": false,
				"error":     "url is required",
			})
		}
		if body.Email == "" {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"status":    "error",
				"connected": false,
				"error":     "email is required",
			})
		}
		if body.Password == "" {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"status":    "error",
				"connected": false,
				"error":     "password is required",
			})
		}

		// Attempt to login
		err := backend.LoginSDK(body.URL, body.Email, body.Password)
		if err != nil {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"status":    "error",
				"connected": false,
				"error":     err.Error(),
			})
		}

		return c.JSON(http.StatusOK, map[string]interface{}{
			"status":    "success",
			"connected": true,
			"url":       backend.AppURL,
		})
	})
}
