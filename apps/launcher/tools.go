package launcher

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"

	"github.com/campbellcharlie/lorg/internal/lorgdb"

	"github.com/campbellcharlie/lorg/internal/utils"
	"github.com/labstack/echo/v4"
	"github.com/rs/xid"
)

type ToolsServerResponse struct {
	Path     string `db:"path" json:"path"`
	Host     string `db:"host" json:"host"`
	ID       string `db:"id" json:"id"`
	Name     string `db:"name" json:"name"`
	Username string `db:"username" json:"username"`
	Password string `db:"password" json:"password"`
}

func (launcher *Launcher) GetToolById(id string) (*lorgdb.Record, error) {
	record, err := launcher.DB.FindRecordById("_tools", id)
	if err != nil {
		return nil, err
	}
	return record, nil
}

func (launcher *Launcher) SetToolData(id, host, state string) (*lorgdb.Record, error) {
	record, err := launcher.DB.FindRecordById("_tools", id)
	if err != nil {
		return nil, err
	}
	record.Set("host", host)
	record.Set("state", state)
	if err := launcher.DB.SaveRecord(record); err != nil {
		return nil, err
	}
	return record, nil
}

func (launcher *Launcher) NewTool(data map[string]any) (*lorgdb.Record, error) {
	record := lorgdb.NewRecord("_tools")
	record.Load(data)

	if err := launcher.DB.SaveRecord(record); err != nil {
		return nil, err
	}

	return record, nil
}

func (launcher *Launcher) ToolsServer(e *echo.Echo) {
	e.GET("/api/tool/server", func(c echo.Context) error {

		var path string
		var hostAddress string
		var name string
		var active bool = false

		var err error

		var toolId string = ""
		var body = make(map[string]any)
		if c.QueryParam("id") != "" {
			body["id"] = c.QueryParam("id")
		} else if err := c.Bind(&body); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
		}

		if id_val, ok := body["id"]; ok {
			toolId = id_val.(string)
		}

		if toolId != "" {
			tool, err := launcher.GetToolById(toolId)
			if err != nil {
				return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
			}

			path = tool.GetString("path")
			state := tool.GetString("state")
			name = tool.GetString("name")

			if state == "active" {
				active = true
				hostAddress = tool.GetString("host")
			} else {
				active = false
				hostAddress, err = utils.CheckAndFindAvailablePort("127.0.0.1:9000")
				if err != nil {
					return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
				}
			}
		} else {
			path = launcher.Config.ProjectsDirectory
			hostAddress, err = utils.CheckAndFindAvailablePort("127.0.0.1:9000")
			name = xid.New().String()
			tool, err := launcher.NewTool(map[string]any{
				"name": name,
				"path": path,
				"host": hostAddress,
				"creds": map[string]any{
					"username": "new@example.com",
					"password": "1234567890",
				},
			})
			if err != nil {
				return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Fail to start new tool"})
			}
			toolId = tool.Id
		}

		fmt.Println("name", name)
		fmt.Println("path", path)
		fmt.Println("host", hostAddress)
		fmt.Println("err", err)

		if err != nil {
			return c.String(http.StatusInternalServerError, err.Error())
		}

		_c := "lorg-tool -path " + path + " -host " + hostAddress + " -name " + name
		launcher.RegisterProcessInDB(
			_c,
			map[string]any{
				"path":     path,
				"host":     hostAddress,
				"name":     name,
				"username": "new@example.com",
				"password": "1234567890",
			},
			"lorg-tool",
			"tool-server",
			"In Queue",
		)

		if !active {
			go launcher.toolsServerStart(hostAddress, path, name, func() {
				fmt.Println("toolsServerStart closed")

				launcher.SetToolData(toolId, "", "closed")

			})
		}

		launcher.SetToolData(toolId, hostAddress, "active")

		return c.JSON(http.StatusOK, ToolsServerResponse{
			Path:     path,
			Host:     hostAddress,
			ID:       toolId,
			Name:     name,
			Username: "new@example.com",
			Password: "1234567890",
		})
	})
}

func (launcher *Launcher) toolsServerStart(hostAddress, path, name string, onClose func()) {

	cmd := exec.Command("lorg-tool", "-path", path, "-host", hostAddress, "-name", name)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("Error executing lorg command: %v\n", err)
		return
	}

	onClose()
}

func (launcher *Launcher) Tools(e *echo.Echo) {
	e.GET("/api/tool", func(c echo.Context) error {
		return echo.NewHTTPError(http.StatusGone, "not supported")
	})
}
