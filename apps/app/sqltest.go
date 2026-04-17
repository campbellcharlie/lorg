package app

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"

	"github.com/labstack/echo/v4"
)

type TEXTSQL struct {
	SQL string `json:"sql"`
}
type CountResult struct {
	CountOfRows         int `db:"CountOfRows" json:"CountOfRows"`
	CountOfDistinctRows int `db:"CountOfDistinctRows" json:"CountOfDistinctRows"`
}

func (backend *Backend) TextSQL(e *echo.Echo) {
	e.POST("/api/sqltest", func(c echo.Context) error {
		if err := requireAuth(c); err != nil {
			return err
		}

		var data TEXTSQL
		if err := c.Bind(&data); err != nil {
			return err
		}

		var results sql.Result
		log.Println("[TextSQL] ", results)

		rows, err := backend.DB.Query(data.SQL)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "Failed")
		}
		defer rows.Close()

		cols, err := rows.Columns()
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "Failed to get columns")
		}

		resultStr := ""
		for rows.Next() {
			// Build a map of column name -> value using standard database/sql scanning
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				continue
			}
			row := make(map[string]any, len(cols))
			for i, col := range cols {
				v := vals[i]
				// Convert []byte to string for JSON serialization
				if b, ok := v.([]byte); ok {
					v = string(b)
				}
				row[col] = v
			}
			jsonStr, _ := json.Marshal(row)
			resultStr = resultStr + string(jsonStr) + "\n"
		}

		return c.JSON(http.StatusOK, resultStr)
	})
}
