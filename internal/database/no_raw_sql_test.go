package database

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// This architectural gate covers production code, tests and migration sources.
// Dependencies generate database statements; application code supplies models,
// quoted identifiers and typed expressions only.
func TestNoRawSQL(t *testing.T) {
	root := filepath.Join("..", "..")
	identifier := regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
	statement := regexp.MustCompile(`(?i)\b(?:select\s+.+\s+from|insert\s+into|update\s+.+\s+set|delete\s+from|create\s+(?:table|index)|alter\s+table|drop\s+table|truncate\s+table|(?:sum|max|min|count|coalesce)\s*\()`)
	forbidden := map[string]bool{"Raw": true, "Exec": true, "QueryContext": true, "QueryRow": true, "QueryRowContext": true, "ExecContext": true, "Expr": true, "NewUnsafeFieldRaw": true}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if strings.HasPrefix(entry.Name(), ".") && path != root || entry.Name() == "node_modules" || entry.Name() == "bin" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) == ".sql" {
			t.Errorf("SQL migration source is forbidden: %s", path)
		}
		if filepath.Ext(path) != ".go" || filepath.Base(path) == "no_raw_sql_test.go" {
			return nil
		}
		positions := token.NewFileSet()
		file, err := parser.ParseFile(positions, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.BasicLit:
				if n.Kind == token.STRING {
					value, _ := strconv.Unquote(n.Value)
					if statement.MatchString(value) {
						t.Errorf("handwritten database expression at %s", positions.Position(n.Pos()))
					}
				}
			case *ast.CompositeLit:
				if typ, ok := n.Type.(*ast.SelectorExpr); ok && (typ.Sel.Name == "Expr" || typ.Sel.Name == "NamedExpr") {
					t.Errorf("raw expression type at %s", positions.Position(n.Pos()))
				}
			case *ast.CallExpr:
				method, ok := n.Fun.(*ast.SelectorExpr)
				if !ok {
					break
				}
				if forbidden[method.Sel.Name] {
					t.Errorf("raw execution method at %s", positions.Position(n.Pos()))
				}
				if len(n.Args) > 0 {
					if literal, ok := n.Args[0].(*ast.BasicLit); ok && literal.Kind == token.STRING {
						value, _ := strconv.Unquote(literal.Value)
						switch method.Sel.Name {
						case "Where", "Or", "Not", "Having", "Joins":
							t.Errorf("string condition at %s", positions.Position(literal.Pos()))
						case "Select", "Group", "Order", "Distinct", "Pluck":
							if !identifier.MatchString(value) {
								t.Errorf("expected column identifier at %s", positions.Position(literal.Pos()))
							}
						}
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
