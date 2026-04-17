package launcher

import (
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

func (launcher *Launcher) TextSQL(e *echo.Echo) {
	e.POST("/api/sqltest", func(c echo.Context) error {
		if err := requireLocalhost(c); err != nil {
			return err
		}

		var data TEXTSQL
		if err := c.Bind(&data); err != nil {
			return err
		}

		log.Println("[TextSQL] ", data.SQL)

		rows, err := launcher.DB.Query(data.SQL)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		defer rows.Close()

		cols, err := rows.Columns()
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}

		resultStr := ""
		for rows.Next() {
			// Scan into a generic slice
			vals := make([]interface{}, len(cols))
			ptrs := make([]interface{}, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				return echo.NewHTTPError(http.StatusBadRequest, err.Error())
			}

			row := make(map[string]interface{}, len(cols))
			for i, col := range cols {
				v := vals[i]
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
