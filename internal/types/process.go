package types

import (
	"encoding/json"
	"fmt"

	"github.com/campbellcharlie/lorg/internal/lorgdb"
	"github.com/campbellcharlie/lorg/internal/utils"
)

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

func RegisterProcessInDB(db *lorgdb.LorgDB, input, data any, state string) string {
	record := lorgdb.NewRecord("_processes")

	id := utils.RandomString(15)

	record.Set("id", id)
	record.Id = id
	record.Set("name", "name")
	record.Set("input", input)
	record.Set("data", data)
	record.Set("state", state)
	record.Set("type", "type")

	err := db.SaveRecord(record)
	utils.CheckErr("[RegisterProcessInDB][SaveRecord]", err)
	return id
}

func SetProcess(db *lorgdb.LorgDB, id, state string) {
	record, err := db.FindRecordById("_processes", id)
	utils.CheckErr("", err)

	record.Set("state", state)

	err = db.SaveRecord(record)
	utils.CheckErr("[RegisterProcessInDB][SaveRecord]", err)
}
