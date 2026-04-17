package tools

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"runtime"

	"github.com/campbellcharlie/lorg/internal/lorgdb"
	"github.com/campbellcharlie/lorg/internal/process"
	"github.com/campbellcharlie/lorg/internal/utils"
	"github.com/labstack/echo/v4"
)

// loop over commandChannel
func (backend *Tools) CommandManager() {
	// log.Println("[CommandManager Stared]")
	for c := range backend.CmdChannel {
		log.Println("Command received: ", c)
		if c.SaveTo == "collection" {
			backend.RunningCommandSaveToCollection(c.ID, c.Command, c.Collection)
		} else {
			backend.RunningCommand(c.ID, c.Command, c.Filename)
		}
	}
}

func (backend *Tools) SetProcess(id, state string) {
	process.SetState(backend.DB, id, state)
}

func (backend *Tools) RegisterProcessInDB(input, data any, name, typz, state string) string {
	return process.RegisterInDB(backend.DB, input, data, name, typz, state)
}

type RunCommandData struct {
	ID         string `db:"id,omitempty" json:"id,omitempty"`
	SaveTo     string `db:"save_to,omitempty" json:"save_to,omitempty"`
	Data       string `db:"data,omitempty" json:"data,omitempty"`
	Command    string `db:"command,omitempty" json:"command,omitempty"`
	Collection string `db:"collection,omitempty" json:"collection,omitempty"`
	Filename   string `db:"filename,omitempty" json:"filename,omitempty"`
}

func (d *RunCommandData) Scan(value interface{}) error {
	if value == nil {
		*d = RunCommandData{}
		return nil
	}
	switch v := value.(type) {
	case []byte:
		return json.Unmarshal(v, d)
	case string:
		return json.Unmarshal([]byte(v), d)
	default:
		return fmt.Errorf("unsupported type: %T", v)
	}
}

// processStateInqueue matches the old schemas.ProcessState.Inqueue constant.
const processStateInqueue = "In Queue"
const processStateRunning = "Running"
const processStateCompleted = "Completed"

func (backend *Tools) RunCommand(e *echo.Echo) {
	e.POST("/api/runcommand", func(c echo.Context) error {
		if err := requireLocalhost(c); err != nil {
			return err
		}

		var data process.RunCommandData
		if err := c.Bind(&data); err != nil {
			return err
		}

		log.Println("[RunCommand]: ", data)

		// Pass both input data and full command data
		id := backend.RegisterProcessInDB(data.Data, data, data.Command, "command", processStateInqueue)

		data.ID = id

		// send to channel
		backend.CmdChannel <- data

		return c.JSON(http.StatusOK, map[string]interface{}{
			"id": id,
		})
	})
}

func (backend *Tools) RunningCommand(id string, command string, filename string) {
	backend.SetProcess(id, processStateRunning)
	var cmd *exec.Cmd
	saveToFile := filename != ""

	var useBash = runtime.GOOS != "windows"

	if saveToFile {
		command = command + " > " + filename
	}

	if useBash {
		cmd = exec.Command("bash", "-c", command)
	} else {
		cmd = exec.Command("cmd", "/C", command)
	}

	log.Println("[RunningCommand] ", cmd)

	// Create a pipe for the output of the command
	_, err := cmd.StdoutPipe()
	if err != nil {
		log.Println("Error creating stdout pipe:", err)
		backend.SetProcess(id, fmt.Sprintf("%v error", err))
		return
	}

	// Start the command
	err = cmd.Start()
	if err != nil {
		log.Println("Error starting command:", err)
		backend.SetProcess(id, fmt.Sprintf("%v error", err))
		return
	}

	// Wait for the command to finish
	err = cmd.Wait()

	if err != nil {
		log.Println("Error waiting for command:", err)
		backend.SetProcess(id, fmt.Sprintf("%v error", err))
		return
	}

	backend.SetProcess(id, processStateCompleted)
}

func (backend *Tools) RunningCommandSaveToCollection(id, command, collectionName string) {
	backend.SetProcess(id, processStateRunning)

	log.Println("RunningCommand: ", command)
	var cmd *exec.Cmd

	var useBash = runtime.GOOS != "windows"

	if useBash {
		cmd = exec.Command("bash", "-c", command)
	} else {
		cmd = exec.Command("cmd", "/C", command)
	}

	// Create a pipe for the output of the command
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Println("Error creating stdout pipe:", err)
		backend.SetProcess(id, fmt.Sprintf("%v error", err))
		return
	}

	// Start the command
	err = cmd.Start()
	if err != nil {
		log.Println("Error starting command:", err)
		backend.SetProcess(id, fmt.Sprintf("%v error", err))
		return
	}

	// Create a scanner to read the output line by line
	scanner := bufio.NewScanner(stdout)

	// Read the output in real-time
	for scanner.Scan() {
		jsonrow := scanner.Text()
		log.Println("[RunningCommand][Scanner]: ", jsonrow)

		record := lorgdb.NewRecord(collectionName)
		record.Set("data", jsonrow)
		err = backend.DB.SaveRecord(record)
		utils.CheckErr("[RunningCommand][SaveRecord]:", err)
	}

	// Wait for the command to finish
	err = cmd.Wait()

	if err != nil {
		log.Println("Error waiting for command:", err)
		backend.SetProcess(id, fmt.Sprintf("%v error", err))
		return
	}

	backend.SetProcess(id, processStateCompleted)
}
