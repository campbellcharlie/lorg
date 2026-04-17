package app

import (
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/campbellcharlie/lorg/internal/lorgdb"

	"github.com/campbellcharlie/lorg/internal/types"
	"github.com/labstack/echo/v4"
)

const SORT_GAP = 1000

func GetSortOrder(items []*lorgdb.Record) int {
	// Calculate new sort order
	newSortOrder := 0
	if items != nil && len(items) > 0 {
		// Find the highest sort order
		maxSortOrder := 0
		for _, item := range items {
			if sortOrder, ok := item.Get("sort_order").(int); ok && sortOrder > maxSortOrder {
				maxSortOrder = sortOrder
			}
		}
		newSortOrder = maxSortOrder + SORT_GAP
	}
	return newSortOrder
}

func (backend *Backend) GetOrCreatePlayground(name string, typeVal string, parentId string) (*lorgdb.Record, error) {
	pgRecord, err := backend.GetRecord("_playground", "name = '"+name+"' AND type = '"+typeVal+"' AND parent_id = '"+parentId+"'")
	if err != nil {
		return nil, err
	}

	if pgRecord == nil {
		pgRecord, err = backend.SaveRecordToCollection("_playground", map[string]interface{}{
			"name":       name,
			"type":       typeVal,
			"parent_id":  parentId,
			"sort_order": 0,
			"expanded":   false,
		})
	}

	return pgRecord, nil
}

func (backend *Backend) PlaygroundNew(e *echo.Echo) {
	e.POST("/api/playground/new", func(c echo.Context) error {
		if err := requireAuth(c); err != nil {
			return err
		}

		log.Println("/api/playground/new")

		var body types.PlaygroundNew
		if err := c.Bind(&body); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
		}

		log.Println("pg body", body)

		name := body.Name

		if name == "" {
			name = "New Playground"
		}
		if body.Type == "" {
			body.Type = "playground"
		}

		// Get all top-level items (parent_id is null)
		topLevelItems, err := backend.DB.FindRecordsSorted("_playground", "parent_id = ?", "sort_order", 0, 0, body.ParentId)

		newSortOrder := GetSortOrder(topLevelItems)

		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
		}

		pgRecord, err := backend.SaveRecordToCollection("_playground", map[string]interface{}{
			"name":       name,
			"type":       body.Type,
			"parent_id":  body.ParentId,
			"sort_order": newSortOrder,
			"expanded":   body.Expanded,
		})

		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
		}

		return c.JSON(http.StatusOK, pgRecord)
	})
}

func (backend *Backend) PlaygroundAddChild(e *echo.Echo) {
	e.POST("/api/playground/add", func(c echo.Context) error {
		if err := requireAuth(c); err != nil {
			return err
		}

		log.Println("/api/playground/add")

		var body types.PlaygroundAdd
		if err := c.Bind(&body); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
		}

		log.Println("pg body", body)

		fmt.Println("Items: ", body.Items)

		// Get all items under the parent to determine sort order
		existingItems, err := backend.DB.FindRecordsSorted("_playground", "parent_id = ?", "sort_order", 0, 0, body.ParentId)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
		}

		newSortOrder := GetSortOrder(existingItems)

		records := []*lorgdb.Record{}

		// Handle list of items
		for _, item := range body.Items {
			fmt.Println("Items loop ", item)
			newSortOrder += SORT_GAP

			pgRecord, err := backend.SaveRecordToCollection("_playground", map[string]interface{}{
				"name":       item.Name,
				"type":       item.Type,
				"parent_id":  body.ParentId,
				"sort_order": newSortOrder,
				"expanded":   false,
			})

			records = append(records, pgRecord)

			if err != nil {
				return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
			}

			switch item.Type {
			case "repeater":
				fmt.Println("PlaygroundItem: ", item)
				err = backend.RepeaterNew(pgRecord.Id, item)
				if err != nil {
					return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
				}
			case "fuzzer":
				fmt.Println("IntruderRequest: ", item)
				err = backend.IntruderNew(pgRecord.Id, item)
				if err != nil {
					return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
				}
			default:
				fmt.Println("Not Found: ")
			}
		}

		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "items": records})
	})
}

func (backend *Backend) PlaygroundDelete(e *echo.Echo) {
	e.POST("/api/playground/delete", func(c echo.Context) error {
		if err := requireAuth(c); err != nil {
			return err
		}

		var body map[string]interface{}
		if err := c.Bind(&body); err != nil {
			return err
		}

		id, ok := body["id"].(string)
		if !ok || id == "" {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Missing or invalid id"})
		}

		// Function to recursively delete children
		var deleteChildren func(parentId string) error
		deleteChildren = func(parentId string) error {
			// Find all children of the current parent
			children, err := backend.DB.FindRecordsSorted("_playground", "parent_id = ?", "sort_order", 0, 0, parentId)
			if err != nil {
				return err
			}

			// Recursively delete each child
			for _, child := range children {
				// Delete children of this child first
				if err := deleteChildren(child.Id); err != nil {
					return err
				}

				// Delete the child record
				if err := backend.DB.DeleteRecord("_playground", child.Id); err != nil {
					return err
				}

				// If the child is a repeater or intruder, delete its associated collection
				childType, _ := child.Get("type").(string)
				switch childType {
				case "repeater":
					if err := backend.RepeaterDelete(child.Id); err != nil {
						return err
					}
				case "fuzzer":
					if err := backend.IntruderDelete(child.Id); err != nil {
						return err
					}
				}
			}
			return nil
		}

		// Get the record to be deleted
		record, err := backend.DB.FindRecordById("_playground", id)
		if err != nil {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Record not found"})
		}

		// Delete all children first
		if err := deleteChildren(id); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
		}

		// Delete the parent record
		if err := backend.DB.DeleteRecord("_playground", record.Id); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
		}

		// If the parent is a repeater or intruder, delete its associated collection
		recordType, _ := record.Get("type").(string)
		switch recordType {
		case "repeater":
			if err := backend.RepeaterDelete(id); err != nil {
				return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
			}
		case "fuzzer":
			if err := backend.IntruderDelete(id); err != nil {
				return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
			}
		}

		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "id": id})
	})
}

func (backend *Backend) RepeaterNew(id string, data types.PlaygroundItem) error {

	// Create repeater_[ID] collection if not exists
	collectionName := "repeater_" + id
	err := backend.CreateCollection(collectionName, []string{
		"url TEXT DEFAULT ''",
		"req TEXT DEFAULT ''",
		"resp TEXT DEFAULT ''",
		"extra TEXT DEFAULT ''",
	})
	if err != nil {
		// If already exists, ignore error
		if !strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return err
		}
	}

	// Insert row into repeater_[ID]
	_, err = backend.SaveRecordToCollection(collectionName, map[string]any{
		"url":   data.ToolData["url"],
		"req":   data.ToolData["req"],
		"resp":  data.ToolData["resp"],
		"data":  data.ToolData,
		"extra": data.ToolData,
	})

	if err != nil {
		return err
	}

	return nil
}

func (backend *Backend) RepeaterDelete(id string) error {
	// Drop the associated repeater_[ID] collection/table
	collectionName := "repeater_" + id
	_, err := backend.DB.Exec("DROP TABLE IF EXISTS \"" + collectionName + "\"")
	return err
}

func (backend *Backend) IntruderNew(id string, data types.PlaygroundItem) error {

	collectionName := "intruder_" + id
	err := backend.CreateCollection(collectionName, []string{
		"url TEXT DEFAULT ''",
		"req TEXT DEFAULT ''",
		"payload TEXT DEFAULT ''",
	})
	if err != nil && !strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return err
	}

	_, err = backend.SaveRecordToCollection(collectionName, map[string]any{
		"url":     data.ToolData["url"],
		"req":     data.ToolData["req"],
		"payload": data.ToolData["payload"],
	})
	if err != nil {
		return err
	}

	return nil
}

func (backend *Backend) IntruderDelete(id string) error {
	// Drop the associated intruder_[ID] collection/table
	collectionName := "intruder_" + id
	_, err := backend.DB.Exec("DROP TABLE IF EXISTS \"" + collectionName + "\"")
	return err
}
