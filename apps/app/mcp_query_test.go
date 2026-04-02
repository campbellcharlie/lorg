package app

import (
	"strings"
	"testing"
)

func TestTokenize(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantLen int // number of tokens excluding EOF
		wantErr bool
	}{
		{"simple field", `req.host.cont:"example.com"`, 3, false},
		{"AND expression", `req.host.cont:"example.com" AND resp.status.eq:200`, 7, false},
		{"OR expression", `req.method.eq:"POST" OR req.method.eq:"PUT"`, 7, false},
		{"parentheses", `(req.host.cont:"test" AND resp.status.eq:200) OR req.method.eq:"GET"`, 13, false},
		{"numeric value", `resp.status.gt:400`, 3, false},
		{"unterminated string", `req.host.cont:"unclosed`, 0, true},
		{"empty input", ``, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokens, err := tokenize(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			nonEOF := 0
			for _, tok := range tokens {
				if tok.typ != tokEOF {
					nonEOF++
				}
			}
			if nonEOF != tt.wantLen {
				t.Errorf("got %d non-EOF tokens, want %d; tokens: %+v", nonEOF, tt.wantLen, tokens)
			}
		})
	}
}

func TestTokenizeEmptyReturnsEOF(t *testing.T) {
	tokens, err := tokenize("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tokens) != 1 || tokens[0].typ != tokEOF {
		t.Errorf("empty input should produce a single EOF token, got %+v", tokens)
	}
}

func TestParseQuery(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"simple comparison", `req.host.cont:"example.com"`, false},
		{"AND", `req.host.cont:"example.com" AND resp.status.eq:200`, false},
		{"OR", `req.method.eq:"POST" OR req.method.eq:"PUT"`, false},
		{"nested parens", `(req.host.cont:"a" OR req.host.cont:"b") AND resp.status.eq:200`, false},
		// "host" has no dots, so tokenizer emits tokValue not tokField -> parser error
		{"invalid field format", `host:"example.com"`, true},
		// empty input hits parseExpr -> parseFactor -> parseComparison -> expect(tokField) on EOF
		{"empty input", ``, true},
		// after colon there is EOF, parser expects a value token
		{"missing value", `req.host.cont:`, true},
		// field with only 2 segments (req.host) lacks operator segment
		{"too few segments", `req.host:"example.com"`, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseQuery(tt.input)
			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestParseQueryAST(t *testing.T) {
	// Verify the AST structure for a simple comparison
	node, err := parseQuery(`req.host.cont:"example.com"`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if node.typ != nodeComparison {
		t.Fatalf("expected nodeComparison, got %d", node.typ)
	}
	if node.field != "req.host" {
		t.Errorf("field = %q, want %q", node.field, "req.host")
	}
	if node.operator != "cont" {
		t.Errorf("operator = %q, want %q", node.operator, "cont")
	}
	if node.value != "example.com" {
		t.Errorf("value = %q, want %q", node.value, "example.com")
	}
}

func TestParseQueryANDAST(t *testing.T) {
	node, err := parseQuery(`req.host.cont:"example.com" AND resp.status.eq:200`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if node.typ != nodeAND {
		t.Fatalf("expected nodeAND at root, got %d", node.typ)
	}
	if node.left == nil || node.right == nil {
		t.Fatal("AND node should have left and right children")
	}
	if node.left.field != "req.host" {
		t.Errorf("left field = %q, want %q", node.left.field, "req.host")
	}
	if node.right.field != "resp.status" {
		t.Errorf("right field = %q, want %q", node.right.field, "resp.status")
	}
}

func TestCompileToSQL(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		wantSubstr     string // substring expected in WHERE clause
		wantNeedsRaw   bool
		wantParamCount int
		wantErr        bool
	}{
		{
			name:           "host contains",
			input:          `req.host.cont:"example.com"`,
			wantSubstr:     "LIKE ? COLLATE NOCASE",
			wantNeedsRaw:   false,
			wantParamCount: 1,
		},
		{
			name:           "status equals numeric",
			input:          `resp.status.eq:200`,
			wantSubstr:     "d.status = ?",
			wantNeedsRaw:   false,
			wantParamCount: 1,
		},
		{
			name:           "body search needs raw join",
			input:          `req.body.cont:"password"`,
			wantSubstr:     "r.request LIKE",
			wantNeedsRaw:   true,
			wantParamCount: 1,
		},
		{
			name:           "numeric gt",
			input:          `resp.status.gt:400`,
			wantSubstr:     "d.status > ?",
			wantNeedsRaw:   false,
			wantParamCount: 1,
		},
		{
			name:           "AND combines clauses",
			input:          `req.host.cont:"example.com" AND resp.status.eq:200`,
			wantSubstr:     "AND",
			wantNeedsRaw:   false,
			wantParamCount: 2,
		},
		{
			name:           "OR combines clauses",
			input:          `req.method.eq:"POST" OR req.method.eq:"PUT"`,
			wantSubstr:     "OR",
			wantNeedsRaw:   false,
			wantParamCount: 2,
		},
		{
			name:    "unknown field",
			input:   `req.nonexistent.eq:"test"`,
			wantErr: true,
		},
		{
			name:    "gt with non-numeric value",
			input:   `resp.status.gt:abc`,
			wantErr: true,
		},
		{
			name:           "neq operator",
			input:          `resp.status.neq:404`,
			wantSubstr:     "d.status != ?",
			wantNeedsRaw:   false,
			wantParamCount: 1,
		},
		{
			name:           "ncont operator",
			input:          `req.host.ncont:"spam"`,
			wantSubstr:     "NOT LIKE ? COLLATE NOCASE",
			wantNeedsRaw:   false,
			wantParamCount: 1,
		},
		{
			name:           "lt operator",
			input:          `resp.status.lt:300`,
			wantSubstr:     "d.status < ?",
			wantNeedsRaw:   false,
			wantParamCount: 1,
		},
		{
			name:           "resp.body needs raw",
			input:          `resp.body.cont:"token"`,
			wantSubstr:     "r.response LIKE",
			wantNeedsRaw:   true,
			wantParamCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ast, err := parseQuery(tt.input)
			if err != nil {
				if tt.wantErr {
					return // parse-level error is acceptable
				}
				t.Fatalf("parse error: %v", err)
			}

			cq, err := compileToSQL(ast)
			if tt.wantErr {
				if err == nil {
					t.Error("expected compile error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("compile error: %v", err)
			}

			if tt.wantSubstr != "" && !strings.Contains(cq.where, tt.wantSubstr) {
				t.Errorf("WHERE clause %q does not contain %q", cq.where, tt.wantSubstr)
			}
			if cq.needsRaw != tt.wantNeedsRaw {
				t.Errorf("needsRaw = %v, want %v", cq.needsRaw, tt.wantNeedsRaw)
			}
			if tt.wantParamCount > 0 && len(cq.params) != tt.wantParamCount {
				t.Errorf("got %d params, want %d", len(cq.params), tt.wantParamCount)
			}
		})
	}
}

func TestCompileToSQLParams(t *testing.T) {
	// Verify that "cont" wraps the value in % wildcards
	ast, err := parseQuery(`req.host.cont:"example.com"`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	cq, err := compileToSQL(ast)
	if err != nil {
		t.Fatalf("compile error: %v", err)
	}
	if len(cq.params) != 1 {
		t.Fatalf("expected 1 param, got %d", len(cq.params))
	}
	param, ok := cq.params[0].(string)
	if !ok {
		t.Fatalf("expected string param, got %T", cq.params[0])
	}
	if param != "%example.com%" {
		t.Errorf("param = %q, want %q", param, "%example.com%")
	}
}

func TestBuildFullSQL(t *testing.T) {
	// Without raw join
	cq := &compiledQuery{where: "d.host LIKE ? COLLATE NOCASE", needsRaw: false}
	sql := buildFullSQL(cq, 50)
	if !strings.Contains(sql, "FROM _data d") {
		t.Error("expected FROM _data d in SQL")
	}
	if strings.Contains(sql, "JOIN _raw") {
		t.Error("did not expect JOIN _raw when needsRaw=false")
	}
	if !strings.Contains(sql, "LIMIT 50") {
		t.Error("expected LIMIT 50 in SQL")
	}

	// With raw join
	cq2 := &compiledQuery{where: "r.request LIKE ? COLLATE NOCASE", needsRaw: true}
	sql2 := buildFullSQL(cq2, 100)
	if !strings.Contains(sql2, "LEFT JOIN _raw r") {
		t.Errorf("expected LEFT JOIN _raw r in SQL, got: %s", sql2)
	}
}
