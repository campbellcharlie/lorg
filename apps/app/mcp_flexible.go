package app

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// flexInt accepts both real JSON integers (1) and string-coerced integers ("1").
// Some MCP clients silently stringify primitive args, which trips strict
// unmarshal on `int` fields with errors like "cannot unmarshal string into
// Go struct field X.page of type int". flexInt absorbs both shapes.
type flexInt int

func (i *flexInt) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	if b[0] == '"' {
		s := strings.Trim(string(b), `"`)
		if s == "" {
			return nil
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			return fmt.Errorf("flexInt: %q is not an integer", s)
		}
		*i = flexInt(n)
		return nil
	}
	var n int
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*i = flexInt(n)
	return nil
}

// Int returns the underlying int. Convenience for callers that want a plain
// `int` after unmarshal.
func (i flexInt) Int() int { return int(i) }

// flexBool accepts true/false, "true"/"false", 0/1.
type flexBool bool

func (b *flexBool) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	if data[0] == '"' {
		s := strings.ToLower(strings.Trim(string(data), `"`))
		switch s {
		case "true", "1", "yes":
			*b = true
		case "false", "0", "no", "":
			*b = false
		default:
			return fmt.Errorf("flexBool: %q is not a boolean", s)
		}
		return nil
	}
	if string(data) == "1" {
		*b = true
		return nil
	}
	if string(data) == "0" {
		*b = false
		return nil
	}
	var v bool
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*b = flexBool(v)
	return nil
}

// Bool returns the underlying bool.
func (b flexBool) Bool() bool { return bool(b) }
