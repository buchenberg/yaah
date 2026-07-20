package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testGoSource is a realistic Go file used as test input.
// It exercises functions, methods, structs, interfaces, consts, and vars.
const testGoSource = `// Package shapes provides geometric primitives.
package shapes

import (
	"fmt"
	"math"
)

// Pi is a well-known constant.
const Pi = 3.14159

const (
	// StatusOK indicates success.
	StatusOK = iota
	// StatusErr indicates failure.
	StatusErr
)

// MaxItems is a package-level variable.
var MaxItems = 100

var (
	// DefaultRadius is the default circle radius.
	DefaultRadius = 1.0
	// DefaultColor is the default shape color.
	DefaultColor = "red"
)

// Shape is the interface all geometric shapes must implement.
type Shape interface {
	Area() float64
	Perimeter() float64
}

// Circle represents a circle with a radius.
type Circle struct {
	Radius float64
	Color  string
}

// NewCircle creates a circle with the given radius.
func NewCircle(radius float64) *Circle {
	return &Circle{Radius: radius, Color: DefaultColor}
}

// Area returns the area of the circle.
func (c *Circle) Area() float64 {
	return Pi * c.Radius * c.Radius
}

// Perimeter returns the circumference of the circle.
func (c *Circle) Perimeter() float64 {
	return 2 * Pi * c.Radius
}

// Rectangle represents an axis-aligned rectangle.
type Rectangle struct {
	Width  float64
	Height float64
}

// Area returns the area of the rectangle.
func (r Rectangle) Area() float64 {
	return r.Width * r.Height
}

// Perimeter returns the perimeter of the rectangle.
func (r Rectangle) Perimeter() float64 {
	return 2*r.Width + 2*r.Height
}
`

func writeTestGoFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	return p
}

func TestGoOutlineTool_Outline(t *testing.T) {
	dir := t.TempDir()
	file := writeTestGoFile(t, dir, "shapes.go", testGoSource)

	gt := &GoOutlineTool{}
	args := `{"action":"outline","file":"` + strings.ReplaceAll(file, `\`, `\\`) + `"}`
	result, err := gt.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	// Check that key entries appear in the outline.
	checks := []string{
		"package shapes",
		"import",
		"math",
		"const Pi",
		"var MaxItems",
		"interface Shape",
		"struct Circle",
		"func NewCircle",
		"func (*Circle).Area",
		"func (*Circle).Perimeter",
		"struct Rectangle",
		"func (Rectangle).Area",
	}
	for _, c := range checks {
		if !strings.Contains(result, c) {
			t.Errorf("outline missing %q:\n%s", c, result)
		}
	}

	t.Logf("outline:\n%s", result)
}

func TestGoOutlineTool_ExtractFunction(t *testing.T) {
	dir := t.TempDir()
	file := writeTestGoFile(t, dir, "shapes.go", testGoSource)

	gt := &GoOutlineTool{}
	args := `{"action":"extract","file":"` + strings.ReplaceAll(file, `\`, `\\`) + `","name":"NewCircle"}`
	result, err := gt.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	checks := []string{
		"func NewCircle",
		"return &Circle{Radius: radius, Color: DefaultColor}",
	}
	for _, c := range checks {
		if !strings.Contains(result, c) {
			t.Errorf("extracted source missing %q:\n%s", c, result)
		}
	}

	t.Logf("extract NewCircle:\n%s", result)
}

func TestGoOutlineTool_ExtractPointerReceiverMethod(t *testing.T) {
	dir := t.TempDir()
	file := writeTestGoFile(t, dir, "shapes.go", testGoSource)

	gt := &GoOutlineTool{}
	args := `{"action":"extract","file":"` + strings.ReplaceAll(file, `\`, `\\`) + `","name":"(*Circle).Area"}`
	result, err := gt.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	if !strings.Contains(result, "Pi * c.Radius * c.Radius") {
		t.Errorf("expected method body in extract:\n%s", result)
	}
	if !strings.Contains(result, "func (c *Circle) Area()") {
		t.Errorf("expected method signature in extract:\n%s", result)
	}

	t.Logf("extract (*Circle).Area:\n%s", result)
}

func TestGoOutlineTool_ExtractValueReceiverMethod(t *testing.T) {
	dir := t.TempDir()
	file := writeTestGoFile(t, dir, "shapes.go", testGoSource)

	gt := &GoOutlineTool{}
	args := `{"action":"extract","file":"` + strings.ReplaceAll(file, `\`, `\\`) + `","name":"(Rectangle).Area"}`
	result, err := gt.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	if !strings.Contains(result, "r.Width * r.Height") {
		t.Errorf("expected method body in extract:\n%s", result)
	}

	t.Logf("extract (Rectangle).Area:\n%s", result)
}

func TestGoOutlineTool_ExtractStruct(t *testing.T) {
	dir := t.TempDir()
	file := writeTestGoFile(t, dir, "shapes.go", testGoSource)

	gt := &GoOutlineTool{}
	args := `{"action":"extract","file":"` + strings.ReplaceAll(file, `\`, `\\`) + `","name":"Circle"}`
	result, err := gt.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	if !strings.Contains(result, "Radius float64") {
		t.Errorf("expected struct fields in extract:\n%s", result)
	}

	t.Logf("extract Circle:\n%s", result)
}

func TestGoOutlineTool_ExtractInterface(t *testing.T) {
	dir := t.TempDir()
	file := writeTestGoFile(t, dir, "shapes.go", testGoSource)

	gt := &GoOutlineTool{}
	args := `{"action":"extract","file":"` + strings.ReplaceAll(file, `\`, `\\`) + `","name":"Shape"}`
	result, err := gt.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	if !strings.Contains(result, "Area() float64") {
		t.Errorf("expected interface methods in extract:\n%s", result)
	}

	t.Logf("extract Shape:\n%s", result)
}

func TestGoOutlineTool_MissingFile(t *testing.T) {
	gt := &GoOutlineTool{}
	_, err := gt.Execute(context.Background(), `{"action":"outline","file":"/nonexistent.go"}`)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestGoOutlineTool_MissingAction(t *testing.T) {
	gt := &GoOutlineTool{}
	_, err := gt.Execute(context.Background(), `{"file":"x.go"}`)
	if err == nil {
		t.Fatal("expected error for missing action")
	}
}

func TestGoOutlineTool_ExtractMissingName(t *testing.T) {
	gt := &GoOutlineTool{}
	_, err := gt.Execute(context.Background(), `{"action":"extract","file":"x.go"}`)
	if err == nil {
		t.Fatal("expected error for extract without name")
	}
}

func TestGoOutlineTool_BadAction(t *testing.T) {
	gt := &GoOutlineTool{}
	_, err := gt.Execute(context.Background(), `{"action":"compile","file":"x.go"}`)
	if err == nil {
		t.Fatal("expected error for bad action")
	}
}

func TestGoOutlineTool_InvalidGoSyntax(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.go")
	os.WriteFile(bad, []byte("package x\nfunc ()"), 0o644)

	gt := &GoOutlineTool{}
	args := `{"action":"outline","file":"` + strings.ReplaceAll(bad, `\`, `\\`) + `"}`
	_, err := gt.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !strings.Contains(err.Error(), "parse error") {
		t.Errorf("expected parse error, got: %v", err)
	}
}

func TestGoOutlineTool_ExtractNotFound(t *testing.T) {
	dir := t.TempDir()
	file := writeTestGoFile(t, dir, "shapes.go", testGoSource)

	gt := &GoOutlineTool{}
	args := `{"action":"extract","file":"` + strings.ReplaceAll(file, `\`, `\\`) + `","name":"DoesNotExist"}`
	_, err := gt.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("expected error for missing symbol")
	}
}

func TestGoOutlineTool_InvalidJSON(t *testing.T) {
	gt := &GoOutlineTool{}
	_, err := gt.Execute(context.Background(), `not json`)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestGoOutlineTool_NameDescriptionSchema(t *testing.T) {
	gt := &GoOutlineTool{}
	if gt.Name() != "go_outline" {
		t.Errorf("Name() = %q, want %q", gt.Name(), "go_outline")
	}
	if gt.Description() == "" {
		t.Error("Description() is empty")
	}
	schema := gt.Schema()
	var s map[string]any
	if err := json.Unmarshal(schema, &s); err != nil {
		t.Fatalf("Schema() is not valid JSON: %v", err)
	}
}
