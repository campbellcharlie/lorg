package sdk

import (
	"fmt"
)

// ProcessInput represents the input field structure for a process
type ProcessInput struct {
	Completed int    `json:"completed"`
	Total     int    `json:"total"`
	Progress  int    `json:"progress"`
	Message   string `json:"message"`
	Error     string `json:"error"`
}

// CreateProcessRequest represents the request to create a new process
type CreateProcessRequest struct {
	ID          string         `json:"id,omitempty"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Type        string         `json:"type"`
	State       string         `json:"state,omitempty"`
	Data        map[string]any `json:"data,omitempty"`
	Input       *ProcessInput  `json:"input,omitempty"`
	ParentID    string         `json:"parent_id,omitempty"`
	GeneratedBy string         `json:"generated_by,omitempty"`
	CreatedBy   string         `json:"created_by,omitempty"`
}

// UpdateProcessRequest represents the request to update a process
type UpdateProcessRequest struct {
	Input *ProcessInput `json:"input,omitempty"`
	State string        `json:"state,omitempty"`
}

// ProgressUpdate represents progress information for updating a process
type ProgressUpdate struct {
	Completed int
	Total     int
	Message   string
	Error     string
	State     string
}

// CreateProcess creates a new process in the _processes collection
func (c *Client) CreateProcess(req CreateProcessRequest) (string, error) {
	// Set defaults
	if req.State == "" {
		req.State = "running"
	}
	if req.Data == nil {
		req.Data = make(map[string]any)
	}
	if req.Input == nil {
		req.Input = &ProcessInput{
			Completed: 0,
			Total:     100,
			Progress:  0,
			Message:   "Starting...",
			Error:     "",
		}
	}

	// Calculate progress percentage
	if req.Input.Total > 0 {
		req.Input.Progress = (req.Input.Completed * 100) / req.Input.Total
	}

	// Create the process record
	body := map[string]any{
		"name":        req.Name,
		"description": req.Description,
		"type":        req.Type,
		"state":       req.State,
		"data":        req.Data,
		"input":       req.Input,
		"output":      make(map[string]any),
	}

	// If ID is provided, include it
	if req.ID != "" {
		body["id"] = req.ID
	}

	// Add new fields if provided
	if req.ParentID != "" {
		body["parent_id"] = req.ParentID
	}
	if req.GeneratedBy != "" {
		body["generated_by"] = req.GeneratedBy
	}
	if req.CreatedBy != "" {
		body["created_by"] = req.CreatedBy
	}

	response, err := c.Create("_processes", body)
	if err != nil {
		return "", fmt.Errorf("failed to create process: %w", err)
	}

	return response.ID, nil
}

// UpdateProcess updates an existing process with progress information
func (c *Client) UpdateProcess(id string, progress ProgressUpdate) error {
	// Calculate progress percentage
	percentage := 0
	if progress.Total > 0 {
		percentage = (progress.Completed * 100) / progress.Total
	}

	// Build update data
	data := map[string]any{
		"input": map[string]any{
			"completed": progress.Completed,
			"total":     progress.Total,
			"progress":  percentage,
			"message":   progress.Message,
			"error":     progress.Error,
		},
	}

	// Include state if provided
	if progress.State != "" {
		data["state"] = progress.State
	}

	return c.Update("_processes", id, data)
}

// CompleteProcess marks a process as completed
func (c *Client) CompleteProcess(id string, message string) error {
	if message == "" {
		message = "Completed"
	}

	return c.UpdateProcess(id, ProgressUpdate{
		Completed: 100,
		Total:     100,
		Message:   message,
		State:     "completed",
	})
}

// FailProcess marks a process as failed with an error message
func (c *Client) FailProcess(id string, errorMsg string) error {
	return c.UpdateProcess(id, ProgressUpdate{
		Completed: 0,
		Total:     100,
		Message:   "Failed",
		Error:     errorMsg,
		State:     "failed",
	})
}

// PauseProcess marks a process as paused
func (c *Client) PauseProcess(id string, message string) error {
	if message == "" {
		message = "Paused"
	}

	return c.Update("_processes", id, map[string]any{
		"state": "paused",
		"input": map[string]any{
			"message": message,
		},
	})
}

// KillProcess marks a process as killed
func (c *Client) KillProcess(id string, message string) error {
	if message == "" {
		message = "Killed"
	}

	return c.Update("_processes", id, map[string]any{
		"state": "killed",
		"input": map[string]any{
			"message": message,
		},
	})
}
