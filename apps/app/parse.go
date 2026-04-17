package app

import (
	"log"
	"net/http"
	"net/url"

	"github.com/campbellcharlie/lorg/lrx/rawhttp"
	"github.com/labstack/echo/v4"
)

type ParseRawRequest struct {
	Request  string `json:"request"`
	Response string `json:"response"`
}

func (backend *Backend) ParseRaw(e *echo.Echo) {
	e.POST("/api/request/parse", func(c echo.Context) error {
		if err := requireAuth(c); err != nil {
			return err
		}

		var reqData ParseRawRequest
		if err := c.Bind(&reqData); err != nil {
			log.Printf("[Parse Raw] Error binding body: %v", err)
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
		}

		result := map[string]any{}

		if reqData.Request != "" {
			parsed := rawhttp.ParseRequest([]byte(reqData.Request))
			parsedURL, _ := url.Parse(parsed.URL)

			query := ""
			path := parsed.URL
			if parsedURL != nil {
				query = parsedURL.RawQuery
				path = parsedURL.Path
			}

			result["request"] = map[string]any{
				"method":  parsed.Method,
				"url":     parsed.URL,
				"path":    path,
				"query":   query,
				"version": parsed.HTTPVersion,
				"headers": parsed.Headers,
				"body":    parsed.Body,
			}
		}

		if reqData.Response != "" {
			parsed := rawhttp.ParseResponse([]byte(reqData.Response))
			result["response"] = map[string]any{
				"status":     parsed.Status,
				"statusFull": parsed.StatusFull,
				"version":    parsed.Version,
				"headers":    parsed.Headers,
				"body":       parsed.Body,
			}
		}

		return c.JSON(http.StatusOK, result)
	})
}
