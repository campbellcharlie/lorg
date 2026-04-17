package app

import (
	"encoding/json"
	"net/http"
	"regexp"

	"github.com/labstack/echo/v4"
)

func (backend *Backend) SearchRegex(e *echo.Echo) {
	e.POST("/api/regex", func(c echo.Context) error {

		var data map[string]interface{}
		if err := c.Bind(&data); err != nil {
			return err
		}

		regex := data["regex"].(string)
		responseBody := data["responseBody"].(string)

		jsonData := make(map[string]any)

		matched, err := regexp.MatchString(regex, responseBody)
		if err != nil {
			jsonData["error"] = err.Error()
			json.Marshal(jsonData)
			return c.JSON(http.StatusOK, jsonData)
		}

		jsonData["matched"] = matched
		json.Marshal(jsonData)
		return c.JSON(http.StatusOK, jsonData)

	})
}
