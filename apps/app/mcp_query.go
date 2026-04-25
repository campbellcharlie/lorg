package app

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---------------------------------------------------------------------------
// Input schemas
// ---------------------------------------------------------------------------

type QueryArgs struct {
	Action string `json:"action" jsonschema:"required,enum=search,enum=explain" jsonschema_description:"search: execute query; explain: return generated SQL"`
	Query  string `json:"query" jsonschema:"required" jsonschema_description:"HTTPQL-like query string, e.g. req.host.cont:\"example.com\" AND resp.status.eq:200"`
	Limit  int    `json:"limit,omitempty" jsonschema_description:"Max results (default 100)"`
}

// ---------------------------------------------------------------------------
// Tokenizer
// ---------------------------------------------------------------------------

type tokenType int

const (
	tokField  tokenType = iota // e.g. req.host.cont
	tokValue                   // "example.com" or 200
	tokAND
	tokOR
	tokLParen
	tokRParen
	tokColon
	tokEOF
)

type token struct {
	typ tokenType
	val string
}

func tokenize(input string) ([]token, error) {
	var tokens []token
	i := 0
	for i < len(input) {
		ch := input[i]

		// Skip whitespace
		if unicode.IsSpace(rune(ch)) {
			i++
			continue
		}

		// Parentheses
		if ch == '(' {
			tokens = append(tokens, token{tokLParen, "("})
			i++
			continue
		}
		if ch == ')' {
			tokens = append(tokens, token{tokRParen, ")"})
			i++
			continue
		}

		// Colon
		if ch == ':' {
			tokens = append(tokens, token{tokColon, ":"})
			i++
			continue
		}

		// Quoted string
		if ch == '"' {
			j := i + 1
			for j < len(input) && input[j] != '"' {
				if input[j] == '\\' {
					j++ // skip escaped char
				}
				j++
			}
			if j >= len(input) {
				return nil, fmt.Errorf("unterminated string at position %d", i)
			}
			val := input[i+1 : j]
			val = strings.ReplaceAll(val, `\"`, `"`)
			tokens = append(tokens, token{tokValue, val})
			i = j + 1
			continue
		}

		// Word (field name, AND, OR, or unquoted value)
		if isWordChar(ch) {
			j := i
			for j < len(input) && isWordChar(input[j]) {
				j++
			}
			word := input[i:j]
			switch strings.ToUpper(word) {
			case "AND":
				tokens = append(tokens, token{tokAND, "AND"})
			case "OR":
				tokens = append(tokens, token{tokOR, "OR"})
			default:
				// Check if it looks like a field (contains dots) or a value
				if strings.Contains(word, ".") {
					tokens = append(tokens, token{tokField, word})
				} else {
					tokens = append(tokens, token{tokValue, word})
				}
			}
			i = j
			continue
		}

		return nil, fmt.Errorf("unexpected character '%c' at position %d", ch, i)
	}

	tokens = append(tokens, token{tokEOF, ""})
	return tokens, nil
}

func isWordChar(ch byte) bool {
	return ch == '.' || ch == '_' || ch == '-' || ch == '*' || ch == '/' ||
		(ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9')
}

// ---------------------------------------------------------------------------
// AST
// ---------------------------------------------------------------------------

type nodeType int

const (
	nodeComparison nodeType = iota
	nodeAND
	nodeOR
)

type astNode struct {
	typ      nodeType
	field    string // e.g. "req.host"
	operator string // e.g. "cont", "eq"
	value    string
	left     *astNode
	right    *astNode
}

// ---------------------------------------------------------------------------
// Parser (recursive descent)
// ---------------------------------------------------------------------------

type parser struct {
	tokens []token
	pos    int
}

func (p *parser) peek() token {
	if p.pos < len(p.tokens) {
		return p.tokens[p.pos]
	}
	return token{tokEOF, ""}
}

func (p *parser) advance() token {
	t := p.peek()
	p.pos++
	return t
}

func (p *parser) expect(typ tokenType) (token, error) {
	t := p.advance()
	if t.typ != typ {
		return t, fmt.Errorf("expected token type %d, got %d (%q)", typ, t.typ, t.val)
	}
	return t, nil
}

// expr = term (OR term)*
func (p *parser) parseExpr() (*astNode, error) {
	left, err := p.parseTerm()
	if err != nil {
		return nil, err
	}
	for p.peek().typ == tokOR {
		p.advance()
		right, err := p.parseTerm()
		if err != nil {
			return nil, err
		}
		left = &astNode{typ: nodeOR, left: left, right: right}
	}
	return left, nil
}

// term = factor (AND factor)*
func (p *parser) parseTerm() (*astNode, error) {
	left, err := p.parseFactor()
	if err != nil {
		return nil, err
	}
	for p.peek().typ == tokAND {
		p.advance()
		right, err := p.parseFactor()
		if err != nil {
			return nil, err
		}
		left = &astNode{typ: nodeAND, left: left, right: right}
	}
	return left, nil
}

// factor = "(" expr ")" | comparison
func (p *parser) parseFactor() (*astNode, error) {
	if p.peek().typ == tokLParen {
		p.advance()
		node, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(tokRParen); err != nil {
			return nil, fmt.Errorf("missing closing parenthesis")
		}
		return node, nil
	}
	return p.parseComparison()
}

// comparison = field ":" value
// field is like "req.host.cont" (the last segment is the operator)
func (p *parser) parseComparison() (*astNode, error) {
	fieldTok, err := p.expect(tokField)
	if err != nil {
		return nil, fmt.Errorf("expected field (e.g. req.host.cont), got %q", p.tokens[p.pos-1].val)
	}

	// Parse field: split into field path and operator
	// e.g. "req.host.cont" -> field="req.host", op="cont"
	parts := strings.Split(fieldTok.val, ".")
	if len(parts) < 3 {
		return nil, fmt.Errorf("invalid field %q: expected format like req.host.cont", fieldTok.val)
	}
	op := parts[len(parts)-1]
	field := strings.Join(parts[:len(parts)-1], ".")

	// Expect colon
	if _, err := p.expect(tokColon); err != nil {
		return nil, fmt.Errorf("expected ':' after field %q", fieldTok.val)
	}

	// Expect value
	valTok, err := p.expect(tokValue)
	if err != nil {
		return nil, fmt.Errorf("expected value after %q:", fieldTok.val)
	}

	return &astNode{
		typ:      nodeComparison,
		field:    field,
		operator: op,
		value:    valTok.val,
	}, nil
}

func parseQuery(input string) (*astNode, error) {
	tokens, err := tokenize(input)
	if err != nil {
		return nil, err
	}
	p := &parser{tokens: tokens}
	node, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if p.peek().typ != tokEOF {
		return nil, fmt.Errorf("unexpected token %q after query", p.peek().val)
	}
	return node, nil
}

// ---------------------------------------------------------------------------
// SQL Compiler
// ---------------------------------------------------------------------------

// fieldMapping maps query fields to SQL expressions over lorgdb's schema.
// _data has flat host/port columns plus req_json / resp_json blobs (we
// use json_extract for nested fields). Raw bytes live in separate _req
// (alias q) and _resp (alias s) tables joined on the same id.
var fieldMapping = map[string]struct {
	expr      string // SQL expression to use in WHERE
	needsReq  bool   // requires JOIN on _req table (alias q)
	needsResp bool   // requires JOIN on _resp table (alias s)
	isText    bool
}{
	"req.host":     {"d.host", false, false, true},
	"req.method":   {"json_extract(d.req_json, '$.method')", false, false, true},
	"req.path":     {"json_extract(d.req_json, '$.path')", false, false, true},
	"req.body":     {"q.raw", true, false, true},
	"req.headers":  {"q.raw", true, false, true},
	"resp.status":  {"CAST(json_extract(d.resp_json, '$.status') AS INTEGER)", false, false, false},
	"resp.body":    {"s.raw", false, true, true},
	"resp.headers": {"s.raw", false, true, true},
	"resp.length":  {"CAST(json_extract(d.resp_json, '$.length') AS INTEGER)", false, false, false},
	"resp.mime":    {"json_extract(d.resp_json, '$.mime')", false, false, true},
}

type compiledQuery struct {
	where     string
	params    []any
	needsReq  bool // true if we need to JOIN the _req table (alias q)
	needsResp bool // true if we need to JOIN the _resp table (alias s)
}

func compileToSQL(node *astNode) (*compiledQuery, error) {
	cq := &compiledQuery{}
	where, err := compileNode(node, cq)
	if err != nil {
		return nil, err
	}
	cq.where = where
	return cq, nil
}

func compileNode(node *astNode, cq *compiledQuery) (string, error) {
	switch node.typ {
	case nodeAND:
		left, err := compileNode(node.left, cq)
		if err != nil {
			return "", err
		}
		right, err := compileNode(node.right, cq)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("(%s AND %s)", left, right), nil

	case nodeOR:
		left, err := compileNode(node.left, cq)
		if err != nil {
			return "", err
		}
		right, err := compileNode(node.right, cq)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("(%s OR %s)", left, right), nil

	case nodeComparison:
		mapping, ok := fieldMapping[node.field]
		if !ok {
			return "", fmt.Errorf("unknown field %q. Valid: %s", node.field, validFieldsList())
		}

		colRef := mapping.expr
		if mapping.needsReq {
			cq.needsReq = true
		}
		if mapping.needsResp {
			cq.needsResp = true
		}

		switch node.operator {
		case "eq":
			if mapping.isText {
				cq.params = append(cq.params, node.value)
			} else {
				v, err := strconv.Atoi(node.value)
				if err != nil {
					cq.params = append(cq.params, node.value)
				} else {
					cq.params = append(cq.params, v)
				}
			}
			return fmt.Sprintf("%s = ?", colRef), nil

		case "neq":
			if mapping.isText {
				cq.params = append(cq.params, node.value)
			} else {
				v, err := strconv.Atoi(node.value)
				if err != nil {
					cq.params = append(cq.params, node.value)
				} else {
					cq.params = append(cq.params, v)
				}
			}
			return fmt.Sprintf("%s != ?", colRef), nil

		case "cont":
			cq.params = append(cq.params, "%"+node.value+"%")
			return fmt.Sprintf("%s LIKE ? COLLATE NOCASE", colRef), nil

		case "ncont":
			cq.params = append(cq.params, "%"+node.value+"%")
			return fmt.Sprintf("%s NOT LIKE ? COLLATE NOCASE", colRef), nil

		case "gt":
			v, err := strconv.Atoi(node.value)
			if err != nil {
				return "", fmt.Errorf("operator 'gt' requires numeric value, got %q", node.value)
			}
			cq.params = append(cq.params, v)
			return fmt.Sprintf("%s > ?", colRef), nil

		case "lt":
			v, err := strconv.Atoi(node.value)
			if err != nil {
				return "", fmt.Errorf("operator 'lt' requires numeric value, got %q", node.value)
			}
			cq.params = append(cq.params, v)
			return fmt.Sprintf("%s < ?", colRef), nil

		case "gte":
			v, err := strconv.Atoi(node.value)
			if err != nil {
				return "", fmt.Errorf("operator 'gte' requires numeric value, got %q", node.value)
			}
			cq.params = append(cq.params, v)
			return fmt.Sprintf("%s >= ?", colRef), nil

		case "lte":
			v, err := strconv.Atoi(node.value)
			if err != nil {
				return "", fmt.Errorf("operator 'lte' requires numeric value, got %q", node.value)
			}
			cq.params = append(cq.params, v)
			return fmt.Sprintf("%s <= ?", colRef), nil

		case "re":
			// Validate the regex at parse time even though SQLite lacks native REGEXP.
			// We fall back to LIKE for the SQL filter and apply true regex post-query.
			if _, err := regexp.Compile(node.value); err != nil {
				return "", fmt.Errorf("invalid regex %q: %v", node.value, err)
			}
			cq.params = append(cq.params, "%"+node.value+"%")
			return fmt.Sprintf("%s LIKE ?", colRef), nil

		default:
			return "", fmt.Errorf("unknown operator %q. Valid: eq, neq, cont, ncont, gt, lt, gte, lte, re", node.operator)
		}

	default:
		return "", fmt.Errorf("unknown node type %d", node.typ)
	}
}

func validFieldsList() string {
	fields := make([]string, 0, len(fieldMapping))
	for f := range fieldMapping {
		fields = append(fields, f)
	}
	return strings.Join(fields, ", ")
}

// ---------------------------------------------------------------------------
// SQL generation helper (shared by search and explain actions)
// ---------------------------------------------------------------------------

func buildFullSQL(cq *compiledQuery, limit int) string {
	// Project the same shape as searchTraffic: id, index, host, method,
	// path, status, length, mime, port. method/path/status/length/mime
	// come from the JSON blobs in _data.
	cols := `d.id, d."index", d.host, d.port,
		json_extract(d.req_json, '$.method')   AS method,
		json_extract(d.req_json, '$.path')     AS path,
		CAST(json_extract(d.resp_json, '$.status') AS INTEGER) AS status,
		CAST(json_extract(d.resp_json, '$.length') AS INTEGER) AS length,
		json_extract(d.resp_json, '$.mime')    AS mime`

	joins := ""
	if cq.needsReq {
		joins += " LEFT JOIN _req q ON d.id = q.id"
	}
	if cq.needsResp {
		joins += " LEFT JOIN _resp s ON d.id = s.id"
	}

	return fmt.Sprintf(
		"SELECT %s FROM _data d%s WHERE %s ORDER BY d.\"index\" DESC LIMIT %d",
		cols, joins, cq.where, limit,
	)
}

// ---------------------------------------------------------------------------
// Query execution
// ---------------------------------------------------------------------------

func (backend *Backend) executeTrafficQuery(query string, limit int) ([]map[string]any, string, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}

	ast, err := parseQuery(query)
	if err != nil {
		return nil, "", fmt.Errorf("parse error: %w", err)
	}

	cq, err := compileToSQL(ast)
	if err != nil {
		return nil, "", fmt.Errorf("compile error: %w", err)
	}

	sql := buildFullSQL(cq, limit)

	// Execute against lorgdb — it owns the canonical _data / _raw tables
	// the query DSL was designed against. The project SQLite uses a
	// different schema (http_traffic / http_messages), so this SQL would
	// fail there with "no such table: _data".
	rows, err := backend.DB.Query(sql, cq.params...)
	if err != nil {
		return nil, sql, fmt.Errorf("query error: %w", err)
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, sql, fmt.Errorf("columns error: %w", err)
	}

	var results []map[string]any
	for rows.Next() {
		values := make([]any, len(columns))
		valuePtrs := make([]any, len(columns))
		for i := range values {
			valuePtrs[i] = &values[i]
		}
		if err := rows.Scan(valuePtrs...); err != nil {
			continue
		}
		row := make(map[string]any)
		for i, col := range columns {
			row[col] = values[i]
		}
		results = append(results, row)
	}

	return results, sql, nil
}

// ---------------------------------------------------------------------------
// MCP Handler
// ---------------------------------------------------------------------------

func (backend *Backend) queryHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args QueryArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	switch args.Action {
	case "search":
		results, sql, err := backend.executeTrafficQuery(args.Query, args.Limit)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcpJSONResult(map[string]any{
			"results": results,
			"count":   len(results),
			"query":   args.Query,
			"sql":     sql,
		})

	case "explain":
		ast, err := parseQuery(args.Query)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("parse error: %v", err)), nil
		}
		cq, err := compileToSQL(ast)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("compile error: %v", err)), nil
		}

		limit := args.Limit
		if limit <= 0 {
			limit = 100
		}

		sql := buildFullSQL(cq, limit)
		paramsJSON, _ := json.Marshal(cq.params)

		return mcpJSONResult(map[string]any{
			"query":     args.Query,
			"sql":       sql,
			"params":    string(paramsJSON),
			"needsReq":  cq.needsReq,
			"needsResp": cq.needsResp,
		})

	default:
		return mcp.NewToolResultError("unknown action: " + args.Action + ". Valid: search, explain"), nil
	}
}
