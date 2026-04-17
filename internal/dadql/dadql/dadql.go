package dadql

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/campbellcharlie/lorg/internal/dadql/fexpr"
)

func Filter(data map[string]any, filter string) (bool, error) {
	if len(filter) == 0 {
		return true, nil
	}
	result, err := fexpr.Parse(filter)

	// fmt.Printf("\n%s\n", filter)
	// fmt.Println(strings.Repeat("─", len(filter)+1))
	// defer fmt.Printf("└%s\n", strings.Repeat("─", len(filter)))
	// fmt.Println("Parsed: ", result)

	if err != nil {
		return false, fmt.Errorf("error: %v", err)
	}

	return HandleFilter(result, data)
}

func FilterJson(jsonString, filter string) (bool, error) {
	// Read the JSON string and parse it into a map
	var data map[string]any
	err := json.Unmarshal([]byte(jsonString), &data)
	if err != nil {
		return false, fmt.Errorf("error: %v", err)
	}

	return Filter(data, filter)
}

func HandleFilter(result []fexpr.ExprGroup, data map[string]any) (bool, error) {
	var previousCondition = false
	var notOperator = false
	var totalGroups = len(result)
	var err error

	for i, group := range result {

		// fmt.Println("Group: ", group)
		// fmt.Println("Join: ", group.Join)
		if i != 0 && totalGroups > 1 {
			if group.Join == fexpr.AndOperator {
				if !previousCondition {
					return false, nil
				}
			} else if group.Join == fexpr.OrOperator {
				if previousCondition {
					return true, nil
				}
			}
		}

		var condition = false

		switch item := group.Item.(type) {
		case fexpr.Expr:
			if strings.Contains(item.Left.Literal, ".") {
				var left string
				var dataset map[string]any

				k := strings.Split(item.Left.Literal, ".")
				klen := len(k)

				tmpdata := data

				for j, key := range k {
					if j < klen-1 {
						left = k[j+1]
						if d, found := tmpdata[key]; found {

							switch d := d.(type) {
							case map[string]any:
								tmpdata = d
							default:
								return false, fmt.Errorf("'%v' is not an object/map", key)
							}
						} else {
							// list other keys
							// fmt.Println("-------------")
							// for k, _ := range data {
							// 	fmt.Println("key: ", k)
							// }
							return false, fmt.Errorf("key '%v' not found", key)
						}
					}
				}

				dataset = tmpdata

				item.Left.Literal = left
				// data = dataset
				condition, err = HandleExpr(item, dataset)
				if err != nil {
					return false, err
				}
			} else {
				condition, err = HandleExpr(item, data)
				if err != nil {
					return false, err
				}
			}
			previousCondition = condition
		case fexpr.ExprGroup:
			condition, err = HandleFilter([]fexpr.ExprGroup{item}, data)
			if err != nil {
				return false, err
			}
			previousCondition = condition
		case []fexpr.ExprGroup:
			condition, err = HandleFilter(item, data)
			if err != nil {
				return false, err
			}
			previousCondition = condition
		case nil:
			notOperator = true
			continue
		default:
			fmt.Println("	Unknown type")
		}

		if notOperator {
			previousCondition = !previousCondition
			notOperator = false
		}
	}

	if notOperator {
		previousCondition = !previousCondition
	}

	return previousCondition, nil
}

// Get integer from interface
func getIntFromInterface(i interface{}) (int, error) {
	switch v := i.(type) {
	case int:
		return v, nil
	case float64:
		return int(v), nil
	case string:
		num, err := strconv.Atoi(v)
		if err != nil {
			return num, fmt.Errorf("error: %v", err)
		}
		return num, nil
	default:
		return 0, fmt.Errorf("[getIntFromInterface] unexpected type '%T'", v)
	}
}

func extractValue(value any, typez fexpr.TokenType, op fexpr.SignOp) (any, error) {

	// if v, ok := value.(string); ok {
	// 	return v, nil
	// }
	// Check if the value matches the expected type
	switch typez {
	case "number":
		if op == fexpr.SignLike || op == fexpr.SignNlike {
			return false, fmt.Errorf("unsupported operator %s for number", op)
		}
		num, err := getIntFromInterface(value)
		if err != nil {
			return false, fmt.Errorf("[extractValue] unexpected type %T", value)
		}
		return num, nil
	case "identifier":
		switch v := value.(type) {
		case string:
			if strings.ToLower(v) == "true" {
				return true, nil
			} else if strings.ToLower(v) == "false" {
				return false, nil
			} else {
				return false, fmt.Errorf("unknown Identifier %v", v)
			}
		case bool:
			if !(op == fexpr.SignEq || op == fexpr.SignNeq) {
				return false, fmt.Errorf("unsupported operator '%s' for boolean", op)
			}
			return v, nil
		default:
			return false, fmt.Errorf("[getBooleanFromInterface] unexpected type '%T'", v)
		}
	case "text", "comment":
		if op == fexpr.SignGt || op == fexpr.SignAnyGte || op == fexpr.SignLt || op == fexpr.SignAnyLte {
			return false, fmt.Errorf("unsupported operator %s for number", op)
		}
		if v, ok := value.(string); ok {
			return v, nil
		}
	case "regex":
		if op == fexpr.SignGt || op == fexpr.SignAnyGte || op == fexpr.SignLt || op == fexpr.SignAnyLte {
			return false, fmt.Errorf("unsupported operator %s for regex", op)
		}
		if v, ok := value.(string); ok {
			return v, nil
		}
	default:
		// Will not reach here
		return false, fmt.Errorf("[extractValue] unsupported type: '%s'", typez)
	}

	return false, fmt.Errorf("%v type:%s mismatch for key", value, typez)
}

func HandleExpr(singleFilter fexpr.Expr, data map[string]interface{}) (bool, error) {
	left, ok := data[singleFilter.Left.Literal]

	if !ok {
		return false, fmt.Errorf("key not found: %s", singleFilter.Left.Literal) // Key not found
	}

	// Based on the type of the right operand
	typez := singleFilter.Right.Type

	leftValue, err := extractValue(left, typez, singleFilter.Op)
	if err != nil {
		return false, err
	}

	rightValue, err := extractValue(singleFilter.Right.Literal, typez, singleFilter.Op)
	if err != nil {
		return false, err
	}

	b, err := CheckConditions(leftValue, singleFilter.Op, rightValue, typez)
	// fmt.Printf("├── %v %v %v: %v\n", leftValue, singleFilter.Op, rightValue, b)
	return b, err
}

func CheckConditions(left any, op fexpr.SignOp, right any, typez fexpr.TokenType) (bool, error) {

	switch op {
	case fexpr.SignEq:
		if typez == fexpr.TokenRegex {
			return false, fmt.Errorf("for regex use ~ operator")
		}
		return HandleEq(left, right), nil
	case fexpr.SignNeq:
		if typez == fexpr.TokenRegex {
			return false, fmt.Errorf("for regex use ~ operator")
		}
		return HandleNeq(left, right), nil
	case fexpr.SignLike:
		if typez == fexpr.TokenRegex {
			return HandleRegex(left, right)
		}
		return HandleLike(left.(string), right.(string)), nil
	case fexpr.SignNlike:
		if typez == fexpr.TokenRegex {
			matched, err := HandleRegex(left, right)
			return !matched, err
		}
		return HandleNlike(left.(string), right.(string)), nil
	case fexpr.SignGt:
		return HandleGt(left.(int), right.(int)), nil
	case fexpr.SignGte:
		return HandleGte(left.(int), right.(int)), nil
	case fexpr.SignLt:
		return HandleLt(left.(int), right.(int)), nil
	case fexpr.SignLte:
		return HandleLte(left.(int), right.(int)), nil
	default:
		return false, nil
	}
}

func CheckString(left any, right any) (string, string, error) {
	var s1 string
	var s2 string

	switch left := left.(type) {
	case string:
		s1 = left
	default:
		return "", "", fmt.Errorf("expected %v to be string", left)
	}

	switch right := right.(type) {
	case string:
		s2 = right
	default:
		return s1, "", fmt.Errorf("expected %v to be string", right)
	}
	return s1, s2, nil
}

// SearchLike compares two strings allowing '%' wildcard similar to SQL.
// Returns true if the strings match, considering '%' as a wildcard.
func SearchLike(s1, s2 string) bool {

	if !strings.Contains(s2, "%") {
		s2 = "%" + s2 + "%"
	}

	// Split the strings at the wildcard '%' character
	parts := strings.Split(s2, "%")

	// Iterate over the parts and check if s1 contains each part in order
	lastIndex := 0
	for _, part := range parts {
		index := strings.Index(s1[lastIndex:], part)
		if index == -1 {
			return false
		}
		lastIndex += index + len(part)
	}

	// If all parts are found in order, return true
	return true
}

func HandleEq(value, compare any) bool {
	return value == compare
}

func HandleNeq(value, compare any) bool {
	return value != compare
}

func HandleLike(value, compare string) bool {
	return SearchLike(value, compare)
}

func HandleRegex(left, right any) (bool, error) {
	s, regex, err := CheckString(left, right)

	if err != nil {
		return false, err
	}

	matched, err := regexp.MatchString(regex, s)
	if err != nil {
		return false, fmt.Errorf("bad regular expression: %q", err)
	}

	return matched, nil
}

func HandleNlike(value, compare string) bool {
	return !SearchLike(value, compare)
}

// Greater than
func HandleGt(value, compare int) bool {
	return value > compare
}

// Greater than or equal to
func HandleGte(value, compare int) bool {
	return value >= compare
}

// Less than
func HandleLt(value, compare int) bool {
	return value < compare
}

// Less than or equal to
func HandleLte(value, compare int) bool {
	return value <= compare
}
