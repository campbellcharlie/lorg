package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/campbellcharlie/lorg/internal/save"
	"github.com/labstack/echo/v4"
)

type Path struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
}

type TemplateInfo struct {
	Name    string `json:"name,omitempty"`
	Content string `json:"content,omitempty"`
}

func (backend *Backend) TemplatesList(e *echo.Echo) {
	e.GET("/api/templates/list", func(c echo.Context) error {

		list := []Path{}

		entries, err := os.ReadDir(backend.Config.TemplateDirectory)
		if err != nil {
			fmt.Println("Error:", err)
			return err
		}
		for _, entry := range entries {
			name := entry.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			if entry.IsDir() {
				continue
			}
			lower := strings.ToLower(name)
			if !(strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml")) {
				continue
			}
			list = append(list, Path{
				Name:  name,
				Path:  path.Join(backend.Config.TemplateDirectory, name),
				IsDir: false,
			})
		}

		jsonData := make(map[string]any)
		jsonData["list"] = list

		json.Marshal(jsonData)
		return c.JSON(http.StatusOK, jsonData)
	})
}

func (backend *Backend) TemplatesNew(e *echo.Echo) {
	e.POST("/api/templates/new", func(c echo.Context) error {

		var data TemplateInfo
		if err := c.Bind(&data); err != nil {
			return err
		}

		filepath := path.Join(backend.Config.TemplateDirectory, data.Name)

		save.WriteFile(filepath, []byte(data.Content))

		return c.JSON(http.StatusOK, map[string]interface{}{
			"filepath": filepath,
		})
	})
}

func (backend *Backend) TemplatesDelete(e *echo.Echo) {
	e.DELETE("/api/templates/:template", func(c echo.Context) error {

		file := c.Param("template")

		filepath := path.Join(backend.Config.TemplateDirectory, file)

		err := os.Remove(filepath)
		if err != nil {
			return c.String(http.StatusInternalServerError, "Error deleting file")
		}

		return c.String(http.StatusOK, "")
	})
}
