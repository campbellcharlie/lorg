package process

import (
	"log"

	"github.com/campbellcharlie/lorg/internal/lorgdb"
	"github.com/campbellcharlie/lorg/internal/utils"
)

// ProcessInput represents the input field structure for a process
type ProcessInput struct {
	Completed int    `json:"completed"`
	Total     int    `json:"total"`
	Progress  int    `json:"progress"`
	Message   string `json:"message"`
	Error     string `json:"error"`
}

// ProgressUpdate represents progress information for updating a process
type ProgressUpdate struct {
	Completed int
	Total     int
	Message   string
	Error     string
	State     string
}

func RegisterInDB(db *lorgdb.LorgDB, input, data any, name, typz, state string) string {
	record := lorgdb.NewRecord("_processes")
	id := utils.RandomString(15)

	log.Println("id", id)
	record.Set("id", id)
	record.Id = id
	log.Println("name", name)
	record.Set("name", name)
	log.Println("input", input)
	record.Set("input", map[string]interface{}{
		"command": input,
	})
	log.Println("data", data)
	record.Set("data", data)
	log.Println("state", state)
	record.Set("state", state)
	log.Println("typz", typz)
	record.Set("type", typz)

	err := db.SaveRecord(record)
	utils.CheckErr("[RegisterProcessInDB][SaveRecord]", err)
	return id
}

// CreateProcess creates a new process with progress tracking
func CreateProcess(db *lorgdb.LorgDB, name, description, typz, state string, data map[string]any, customID string) string {
	record := lorgdb.NewRecord("_processes")

	id := customID
	if id == "" {
		id = utils.RandomString(15)
	}

	if state == "" {
		state = "running"
	}
	if data == nil {
		data = make(map[string]any)
	}

	record.Set("id", id)
	record.Id = id
	record.Set("name", name)
	record.Set("description", description)
	record.Set("type", typz)
	record.Set("state", state)
	record.Set("data", data)
	record.Set("input", map[string]interface{}{
		"completed": 0,
		"total":     100,
		"progress":  0,
		"message":   "Starting...",
		"error":     "",
	})
	record.Set("output", map[string]interface{}{})

	err := db.SaveRecord(record)
	utils.CheckErr("[CreateProcess][SaveRecord]", err)
	return id
}

func GetProcess(db *lorgdb.LorgDB, id string) (*lorgdb.Record, error) {
	return db.FindRecordById("_processes", id)
}

func SetState(db *lorgdb.LorgDB, id, state string) {
	record, err := db.FindRecordById("_processes", id)
	utils.CheckErr("", err)

	record.Set("state", state)

	err = db.SaveRecord(record)
	utils.CheckErr("[RegisterProcessInDB][SaveRecord]", err)
}

// UpdateProgress updates the progress of a process
func UpdateProgress(db *lorgdb.LorgDB, id string, progress ProgressUpdate) {
	record, err := db.FindRecordById("_processes", id)
	utils.CheckErr("[UpdateProgress][FindRecord]", err)

	percentage := 0
	if progress.Total > 0 {
		percentage = (progress.Completed * 100) / progress.Total
	}

	record.Set("input", map[string]interface{}{
		"completed": progress.Completed,
		"total":     progress.Total,
		"progress":  percentage,
		"message":   progress.Message,
		"error":     progress.Error,
	})

	if progress.State != "" {
		record.Set("state", progress.State)
	}

	err = db.SaveRecord(record)
	utils.CheckErr("[UpdateProgress][SaveRecord]", err)
}

// CompleteProcess marks a process as completed
func CompleteProcess(db *lorgdb.LorgDB, id string, message string) {
	if message == "" {
		message = "Completed"
	}

	UpdateProgress(db, id, ProgressUpdate{
		Completed: 100,
		Total:     100,
		Message:   message,
		State:     "completed",
	})
}

// FailProcess marks a process as failed with an error message
func FailProcess(db *lorgdb.LorgDB, id string, errorMsg string) {
	UpdateProgress(db, id, ProgressUpdate{
		Completed: 0,
		Total:     100,
		Message:   "Failed",
		Error:     errorMsg,
		State:     "failed",
	})
}
