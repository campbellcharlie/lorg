package app

import (
	"log"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/campbellcharlie/lorg/internal/save"
	"github.com/labstack/echo/v4"
)

func (backend *Backend) GetFilePath(folder, fileName string) string {
	switch folder {
	case "cache":
		return path.Join(backend.Config.CacheDirectory, fileName)
	case "config":
		return path.Join(backend.Config.ProjectsDirectory, fileName)
	case "cwd":
		cwd, _ := os.Getwd()
		return path.Join(strings.Trim(cwd, " "), fileName)
	default:
		return fileName
	}
}

func (backend *Backend) ReadFile(e *echo.Echo) {
	e.POST("/api/readfile", func(c echo.Context) error {
		if err := requireAuth(c); err != nil {
			return err
		}

		var data map[string]interface{}
		if err := c.Bind(&data); err != nil {
			return err
		}
		log.Println("[ReadFile]: ", data)
		fileName := data["fileName"].(string)
		fileName = strings.Trim(fileName, " ")
		folder := data["folder"].(string)

		content := save.ReadFile(backend.GetFilePath(folder, fileName))

		return c.JSON(http.StatusOK, map[string]interface{}{
			"filecontent": string(content),
		})
	})
}

func (backend *Backend) SaveFile(e *echo.Echo) {
	e.POST("/api/savefile", func(c echo.Context) error {
		if err := requireAuth(c); err != nil {
			return err
		}

		var data map[string]interface{}
		if err := c.Bind(&data); err != nil {
			return err
		}
		fileName := data["fileName"].(string)
		fileData := data["fileData"].(string)
		folder := data["folder"].(string)

		filepath := backend.GetFilePath(folder, fileName)

		// Save request_id.txt
		save.WriteFile(filepath, []byte(fileData))

		return c.JSON(http.StatusOK, map[string]interface{}{
			"filepath": filepath,
		})
	})
}
