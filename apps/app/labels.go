package app

import (
	"log"
	"net/http"

	"github.com/campbellcharlie/lorg/internal/lorgdb"
	"github.com/campbellcharlie/lorg/internal/types"
	"github.com/labstack/echo/v4"
)

func (backend *Backend) LabelNew(e *echo.Echo) {
	e.POST("/api/label/new", func(c echo.Context) error {
		if err := requireAuth(c); err != nil {
			return err
		}

		var data types.Label
		if err := c.Bind(&data); err != nil {
			return err
		}

		record := lorgdb.NewRecord("_labels")
		record.Set("name", data.Name)
		record.Set("color", data.Color)
		record.Set("type", data.Type)

		if err := backend.DB.SaveRecord(record); err != nil {
			record, err2 := backend.DB.FindFirstRecord(
				"_labels", "name = ?", data.Name,
			)
			if err2 != nil {
				return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err2.Error()})
			}
			return c.JSON(http.StatusOK, map[string]interface{}{
				"id":            record.Id,
				"alreadyExists": true,
			})
		}

		return c.JSON(http.StatusOK, map[string]interface{}{
			"id":            record.Id,
			"alreadyExists": false,
		})
	})
}

func (backend *Backend) LabelDelete(e *echo.Echo) {
	e.POST("/api/label/delete", func(c echo.Context) error {
		if err := requireAuth(c); err != nil {
			return err
		}

		var data types.Label
		var err error
		var record *lorgdb.Record

		if err = c.Bind(&data); err != nil {
			log.Println("Label Delete: ", err)
			return err
		}

		if data.ID != "" {
			record, err = backend.DB.FindRecordById("_labels", data.ID)
			if err != nil {
				log.Println("Label Delete: ", err)
				return err
			}
		}

		if data.Name != "" {
			record, err = backend.DB.FindFirstRecord(
				"_labels", "name = ?", data.Name,
			)
			if err != nil {
				log.Println("Label Delete: ", err)
				return err
			}
		}

		// Drop the per-label table (replaces FindCollectionByNameOrId + DeleteCollection)
		if _, err := backend.DB.Exec("DROP TABLE IF EXISTS \"label_" + record.Id + "\""); err != nil {
			log.Println("Label Delete: ", err)
			return err
		}
		if err := backend.DB.DeleteRecord("_labels", record.Id); err != nil {
			log.Println("Label Delete: - Record", err)
			return err
		}

		return c.String(http.StatusOK, "Deleted")
	})
}

func (backend *Backend) LabelAttach(e *echo.Echo) {
	e.POST("/api/label/attach", func(c echo.Context) error {
		if err := requireAuth(c); err != nil {
			return err
		}

		var data types.Label
		if err := c.Bind(&data); err != nil {
			log.Println("[LabelNew]: ", err)
			return err
		}

		// Saving to main collection if doesn't exists
		record := lorgdb.NewRecord("_labels")

		// set individual fields
		// or bulk load with record.Load(map[string]any{...})
		record.Set("name", data.Name)
		record.Set("color", data.Color)
		record.Set("type", data.Type)

		err := backend.DB.SaveRecord(record)
		// =====================

		// Fetching ID
		// TOOD: we should have list of labels here with ids, instead of fetching it every time
		labelRecord, err2 := backend.DB.FindFirstRecord("_labels", "name = ?", data.Name)

		if err2 != nil {
			log.Println("[LabelNew]: ", err)
			return err
		}


		// log.Println("[LabelNew]: ", result2)

		// Attaching to the row
		record3, err := backend.DB.FindRecordById("_attached", data.ID)
		if err != nil {
			log.Println("[LabelNew]: ", err)
			return err
		}

		// Extract existing labels as []string from the JSON-parsed []any
		var currentLabels []string
		if raw := record3.Get("labels"); raw != nil {
			if arr, ok := raw.([]any); ok {
				for _, v := range arr {
					if s, ok := v.(string); ok {
						currentLabels = append(currentLabels, s)
					}
				}
			}
		}
		record3.Set("labels", append(currentLabels, labelRecord.Id))

		if err := backend.DB.SaveRecord(record3); err != nil {
			log.Println("[LabelNew]: ", err)
			return err
		}

		// Increment counter for this label
		backend.CounterManager.Increment("label:"+labelRecord.Id, "_labels", "")

		return c.String(http.StatusOK, "Created")
	})
}
