package launcher

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path"
	"strings"

	"github.com/campbellcharlie/lorg/internal/lorgdb"
	"github.com/campbellcharlie/lorg/internal/utils"
	"github.com/labstack/echo/v4"
	"github.com/rs/xid"
)

func (launcher *Launcher) API_ListProjects(e *echo.Echo) {
	e.GET("/api/project/list", func(c echo.Context) error {
		records, err := launcher.DB.FindRecords("_projects", "1=1")
		if err != nil {
			fmt.Println("Error fetching projects:", err)
			return c.String(http.StatusInternalServerError, "Error fetching projects")
		}

		return c.JSON(http.StatusOK, records)
	})
}

func (launcher *Launcher) API_CreateNewProject(e *echo.Echo) {
	e.POST("/api/project/new", func(c echo.Context) error {
		var data struct {
			Name string `json:"name"`
		}

		if err := c.Bind(&data); err != nil {
			return c.String(http.StatusBadRequest, "Invalid request body")
		}

		if data.Name == "" || strings.TrimSpace(data.Name) == "" {
			return c.String(http.StatusBadRequest, "Project name cannot be empty or just whitespace")
		}

		projectData, err := launcher.CreateNewProject(data.Name)
		if err != nil {
			return c.String(http.StatusInternalServerError, "Error creating project")
		}

		return c.JSON(http.StatusOK, projectData)
	})
}

func (launcher *Launcher) API_OpenProject(e *echo.Echo) {
	e.POST("/api/project/open", func(c echo.Context) error {
		var data struct {
			Project string `json:"project"`
		}

		if err := c.Bind(&data); err != nil {
			return c.String(http.StatusBadRequest, "Invalid request body")
		}

		if data.Project == "" || strings.TrimSpace(data.Project) == "" {
			return c.String(http.StatusBadRequest, "Project can't be empty, send name or id")
		}

		projectData, err := launcher.OpenProjectFromNameOrId(data.Project)
		if err != nil {
			return c.String(http.StatusInternalServerError, err.Error())
		}

		return c.JSON(http.StatusOK, projectData)
	})
}

var ProjectState = struct {
	Active   string
	Unactive string
}{
	Active:   "active",
	Unactive: "unactive",
}

type ProjectStateData struct {
	Ip    string `json:"ip" db:"ip"`
	State string `json:"state" db:"state"`
}

type ProjectData struct {
	Id      string           `json:"id" db:"id"`
	Name    string           `json:"name" db:"name"`
	Path    string           `json:"path" db:"path"`
	Data    ProjectStateData `json:"data" db:"data"`
	Version string           `json:"version" db:"version"`
}

func (launcher *Launcher) ListProjects() {
	fmt.Println("Listing projects")
	records, err := launcher.DB.FindRecords("_projects", "1=1")
	if err != nil {
		fmt.Println("Error fetching projects:", err)
		return
	}

	fmt.Println("\nProjects:")
	for i, record := range records {
		name := fmt.Sprintf("%v", record.Get("name"))
		path := fmt.Sprintf("%v", record.Get("path"))
		fmt.Printf("%d. %-15s (%s)\n", i+1, name, path)
	}
}

func (launcher *Launcher) CreateNewProject(projectName string) (ProjectData, error) {
	ProjectIP, err := utils.CheckAndFindAvailablePort("127.0.0.1:8091")
	if err != nil {
		fmt.Println("Error fetching project IP:", err)
		return ProjectData{}, err
	}

	projectId := xid.New().String()
	projectPath := path.Join(launcher.Config.ProjectsDirectory, projectId)
	os.MkdirAll(projectPath, 0755)

	projectData := ProjectData{
		Id:   projectId,
		Name: projectName,
		Path: projectPath,
		Data: ProjectStateData{
			Ip:    ProjectIP,
			State: ProjectState.Active,
		},
	}

	record := lorgdb.NewRecord("_projects")
	record.Set("name", projectName)
	record.Set("id", projectId)
	record.Set("path", projectPath)
	record.Set("data", projectData.Data)

	err = launcher.DB.SaveRecord(record)
	if err != nil {
		fmt.Println("Error creating project:", err)
		return ProjectData{}, err
	}

	go StartProject(projectPath, ProjectIP, "127.0.0.1:8888", func() {
		launcher.setProjectStateClose(projectId)
	})

	fmt.Println("Project created successfully")
	return projectData, nil
}

func (launcher *Launcher) setProjectStateClose(projectId string) {
	record, err := launcher.DB.FindRecordById("_projects", projectId)
	if err != nil {
		fmt.Println("Error fetching project:", err)
		return
	}

	stateData := ProjectStateData{
		Ip:    "",
		State: ProjectState.Unactive,
	}
	record.Set("data", stateData)

	err = launcher.DB.SaveRecord(record)
	if err != nil {
		fmt.Println("Error saving project state:", err)
		return
	}
}

func (launcher *Launcher) OpenProject(projectIndex int) (ProjectData, error) {
	records, err := launcher.DB.FindRecords("_projects", "1=1")
	if err != nil {
		fmt.Println("Error fetching projects:", err)
		return ProjectData{}, err
	}

	_record_id := records[projectIndex].Get("id")

	record, err := launcher.DB.FindRecordById("_projects", _record_id.(string))
	if err != nil {
		fmt.Println("Error fetching project:", err)
		return ProjectData{}, err
	}

	// Check if project is already running
	var existingStateData ProjectStateData
	dataInterface := record.Get("data")
	if dataInterface != nil {
		jsonData, err := json.Marshal(dataInterface)
		if err == nil {
			if err := json.Unmarshal(jsonData, &existingStateData); err == nil {
				if existingStateData.State == ProjectState.Active && existingStateData.Ip != "" {
					return ProjectData{
						Id:   _record_id.(string),
						Name: record.GetString("name"),
						Path: record.GetString("path"),
						Data: existingStateData,
					}, nil
				}
			}
		}
	}

	ProjectIP, err := utils.CheckAndFindAvailablePort("127.0.0.1:8091")
	if err != nil {
		fmt.Println("Error fetching project IP:", err)
		return ProjectData{}, err
	}

	projectData := ProjectData{
		Id:   _record_id.(string),
		Name: record.GetString("name"),
		Path: record.GetString("path"),
		Data: ProjectStateData{
			Ip:    ProjectIP,
			State: ProjectState.Active,
		},
	}

	record.Set("data", projectData.Data)

	err = launcher.DB.SaveRecord(record)
	if err != nil {
		fmt.Printf("Error saving project state: %v\n", err)
		return ProjectData{}, err
	}

	projectPath := record.GetString("path")
	if projectPath == "" {
		fmt.Println("Error: Project path is empty")
		return ProjectData{}, fmt.Errorf("project path is empty")
	}

	go StartProject(projectPath, ProjectIP, "127.0.0.1:8888", func() {
		launcher.setProjectStateClose(record.GetString("id"))
	})

	fmt.Println("Project opened successfully")
	return projectData, nil
}

func (launcher *Launcher) OpenProjectFromNameOrId(project string) (ProjectData, error) {
	record, err := launcher.DB.FindFirstRecord("_projects", "name = ? OR id = ?", project, project)
	if record == nil || err != nil {
		return ProjectData{}, err
	}

	// Check if project is already running
	var existingStateData ProjectStateData
	dataInterface := record.Get("data")
	if dataInterface != nil {
		jsonData, err := json.Marshal(dataInterface)
		if err == nil {
			if err := json.Unmarshal(jsonData, &existingStateData); err == nil {
				if existingStateData.State == ProjectState.Active && existingStateData.Ip != "" {
					return ProjectData{
						Id:   record.GetString("id"),
						Name: record.GetString("name"),
						Path: record.GetString("path"),
						Data: existingStateData,
					}, nil
				}
			}
		}
	}

	projectIp, err := utils.CheckAndFindAvailablePort("127.0.0.1:8091")
	if err != nil {
		fmt.Println("Error fetching project IP:", err)
		return ProjectData{}, err
	}

	projectStateData := ProjectStateData{
		Ip:    projectIp,
		State: ProjectState.Active,
	}

	projectData := ProjectData{
		Id:   record.GetString("id"),
		Name: record.GetString("name"),
		Path: record.GetString("path"),
		Data: projectStateData,
	}

	record.Set("data", projectStateData)

	err = launcher.DB.SaveRecord(record)
	if err != nil {
		fmt.Printf("Error saving project state: %v\n", err)
		return ProjectData{}, err
	}

	projectPath := record.GetString("path")
	if projectPath == "" {
		fmt.Println("Error: Project path is empty")
		return ProjectData{}, fmt.Errorf("project path is empty")
	}

	go StartProject(projectPath, projectIp, "127.0.0.1:8888", func() {
		launcher.setProjectStateClose(record.GetString("id"))
	})

	fmt.Println("Project opened successfully")
	return projectData, nil
}

func StartProject(projectPath string, host string, proxy string, onClose func()) {
	cmd := exec.Command("lorg", "-path", projectPath, "-host", host, "-proxy", proxy, "-log")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("Error executing lorg command: %v\n", err)
		return
	}

	onClose()
}

func (launcher *Launcher) ResetToolsStates() error {
	records, err := launcher.DB.FindRecords("_tools", "1=1")
	if err != nil {
		fmt.Println("Error fetching tools:", err)
		return err
	}

	for _, record := range records {
		record.Set("host", "")
		record.Set("state", "")

		err = launcher.DB.SaveRecord(record)
		if err != nil {
			fmt.Println("Error saving tool state:", err)
			return err
		}
	}

	fmt.Println("Tools states reset successfully")
	return nil
}

func (launcher *Launcher) ResetProjectStates() error {
	records, err := launcher.DB.FindRecords("_projects", "1=1")
	if err != nil {
		fmt.Println("Error fetching projects:", err)
		return err
	}

	for _, record := range records {
		var projectStateData ProjectStateData
		dataInterface := record.Get("data")
		if dataInterface == nil {
			continue
		}

		jsonData, err := json.Marshal(dataInterface)
		if err != nil {
			fmt.Printf("Error marshaling data for record %s: %v\n", record.Id, err)
			continue
		}

		if err := json.Unmarshal(jsonData, &projectStateData); err != nil {
			fmt.Printf("Error parsing project data for record %s: %v\n", record.Id, err)
			continue
		}

		if projectStateData.State != ProjectState.Active {
			continue
		}

		record.Set("data", ProjectStateData{
			Ip:    "",
			State: ProjectState.Unactive,
		})

		err = launcher.DB.SaveRecord(record)
		if err != nil {
			fmt.Println("Error saving project state:", err)
			return err
		}
	}

	fmt.Println("Project states reset successfully")
	return nil
}
