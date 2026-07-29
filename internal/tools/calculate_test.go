package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestCalculateTool_Basic(t *testing.T) {
	ct := &CalculateTool{}

	tests := []struct {
		expr   string
		expect string
	}{
		{"2 + 3", "5"},
		{"10 - 7", "3"},
		{"4 * 5", "20"},
		{"20 / 4", "5"},
		{"2 ** 8", "256"},
		{"10 % 3", "1"},
		{"2 + 3 * 4", "14"},
		{"(2 + 3) * 4", "20"},
		{"-5 + 3", "-2"},
		{"5 + -3", "2"},
		{"2 * (3 + 4)", "14"},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			result, err := ct.Execute(context.Background(), `{"expr":"`+tt.expr+`"}`)
			if err != nil {
				t.Fatalf("Execute() error: %v", err)
			}
			if result != tt.expect {
				t.Errorf("%s = %q, want %q", tt.expr, result, tt.expect)
			}
		})
	}
}

func TestCalculateTool_FloatingPoint(t *testing.T) {
	ct := &CalculateTool{}

	tests := []struct {
		expr   string
		expect string
	}{
		{"0.1 + 0.2", "0.30000000000000004"},
		{"1.5 * 2", "3"},
		{"3 / 2", "1.5"},
		{"sqrt(2)", "1.4142135623730951"},
		{".5 + .5", "1"},
		{"1.5e2", "150"},
		{"2e-3", "0.002"},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			result, err := ct.Execute(context.Background(), `{"expr":"`+tt.expr+`"}`)
			if err != nil {
				t.Fatalf("Execute() error: %v", err)
			}
			if result != tt.expect {
				t.Errorf("%s = %q, want %q", tt.expr, result, tt.expect)
			}
		})
	}
}

func TestCalculateTool_Constants(t *testing.T) {
	ct := &CalculateTool{}

	tests := []struct {
		expr   string
		expect string
	}{
		{"pi", "3.141592653589793"},
		{"e", "2.718281828459045"},
		{"pi * 0", "0"},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			result, err := ct.Execute(context.Background(), `{"expr":"`+tt.expr+`"}`)
			if err != nil {
				t.Fatalf("Execute() error: %v", err)
			}
			if result != tt.expect {
				t.Errorf("%s = %q, want %q", tt.expr, result, tt.expect)
			}
		})
	}
}

func TestCalculateTool_Functions1Arg(t *testing.T) {
	ct := &CalculateTool{}

	tests := []struct {
		expr   string
		expect string
	}{
		{"abs(-5)", "5"},
		{"abs(5)", "5"},
		{"ceil(3.2)", "4"},
		{"floor(3.9)", "3"},
		{"round(3.6)", "4"},
		{"round(3.4)", "3"},
		{"log(1)", "0"},
		{"exp(0)", "1"},
		{"sin(0)", "0"},
		{"cos(0)", "1"},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			result, err := ct.Execute(context.Background(), `{"expr":"`+tt.expr+`"}`)
			if err != nil {
				t.Fatalf("Execute() error: %v", err)
			}
			if result != tt.expect {
				t.Errorf("%s = %q, want %q", tt.expr, result, tt.expect)
			}
		})
	}
}

func TestCalculateTool_Functions2Args(t *testing.T) {
	ct := &CalculateTool{}

	tests := []struct {
		expr   string
		expect string
	}{
		{"min(3, 7)", "3"},
		{"max(3, 7)", "7"},
		{"min(3, 7, 1, 9, 2)", "1"},
		{"max(3, 7, 1, 9, 2)", "9"},
		{"pow(2, 10)", "1024"},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			result, err := ct.Execute(context.Background(), `{"expr":"`+tt.expr+`"}`)
			if err != nil {
				t.Fatalf("Execute() error: %v", err)
			}
			if result != tt.expect {
				t.Errorf("%s = %q, want %q", tt.expr, result, tt.expect)
			}
		})
	}
}

func TestCalculateTool_NestedFunctions(t *testing.T) {
	ct := &CalculateTool{}

	result, err := ct.Execute(context.Background(), `{"expr":"sqrt(abs(-16))"}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result != "4" {
		t.Errorf("sqrt(abs(-16)) = %q, want 4", result)
	}

	result, err = ct.Execute(context.Background(), `{"expr":"max(sqrt(25), pow(2, 3))"}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result != "8" {
		t.Errorf("max(sqrt(25), pow(2,3)) = %q, want 8", result)
	}
}

func TestCalculateTool_HexBinaryOctal(t *testing.T) {
	ct := &CalculateTool{}

	tests := []struct {
		expr   string
		expect string
	}{
		{"0xFF", "255"},
		{"0xff", "255"},
		{"0b1010", "10"},
		{"0o777", "511"},
		{"0xFF + 1", "256"},
		{"0b1111 & 0b1010", "10"},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			result, err := ct.Execute(context.Background(), `{"expr":"`+tt.expr+`"}`)
			if err != nil {
				t.Fatalf("Execute() error: %v", err)
			}
			if result != tt.expect {
				t.Errorf("%s = %q, want %q", tt.expr, result, tt.expect)
			}
		})
	}
}

func TestCalculateTool_Bitwise(t *testing.T) {
	ct := &CalculateTool{}

	tests := []struct {
		expr   string
		expect string
	}{
		{"5 & 3", "1"},
		{"5 | 3", "7"},
		{"5 ^ 3", "6"},
		{"~0", "-1"},
		{"1 << 4", "16"},
		{"16 >> 2", "4"},
		{"!0", "1"},
		{"!1", "0"},
		{"!5", "0"},
		{"~~5", "5"},
		{"-8 >> 1", "-4"},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			result, err := ct.Execute(context.Background(), `{"expr":"`+tt.expr+`"}`)
			if err != nil {
				t.Fatalf("Execute() error: %v", err)
			}
			if result != tt.expect {
				t.Errorf("%s = %q, want %q", tt.expr, result, tt.expect)
			}
		})
	}
}

func TestCalculateTool_OperatorPrecedence(t *testing.T) {
	ct := &CalculateTool{}

	tests := []struct {
		expr   string
		expect string
	}{
		{"2 ** 3 ** 2", "512"}, // right-assoc: 2^(3^2) = 2^9
		{"2 + 3 * 4", "14"},    // * before +
		{"2 * 3 + 4", "10"},    // * before +
		{"1 << 2 + 1", "8"},    // << binds looser than +
		{"2 ** 3 + 1", "9"},    // ** before +
		{"-2 ** 4", "-16"},     // unary before **
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			result, err := ct.Execute(context.Background(), `{"expr":"`+tt.expr+`"}`)
			if err != nil {
				t.Fatalf("Execute() error: %v", err)
			}
			if result != tt.expect {
				t.Errorf("%s = %q, want %q", tt.expr, result, tt.expect)
			}
		})
	}
}

func TestCalculateTool_Errors(t *testing.T) {
	ct := &CalculateTool{}

	tests := []string{
		`{"expr":"1 / 0"}`,
		`{"expr":"1 % 0"}`,
		`{"expr":"unknown(1)"}`,
		`{"expr":"1 + "}`,
		`{"expr":"(1 + 2"}`,
		`{"expr":"foo"}`,
		`{}`,
		`{"expr":""}`,
		`{"expr":"min(5)"}`,
		`{"expr":"max()"}`,
		`not json`,
	}
	for _, tt := range tests {
		t.Run(tt, func(t *testing.T) {
			_, err := ct.Execute(context.Background(), tt)
			if err == nil {
				t.Errorf("expected error for %q", tt)
			}
		})
	}
}

func TestCalculateTool_Whitespace(t *testing.T) {
	ct := &CalculateTool{}

	result, err := ct.Execute(context.Background(), `{"expr":"  2  +   3  "}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result != "5" {
		t.Errorf("= %q, want 5", result)
	}
}

func TestCalculateTool_InfNaN(t *testing.T) {
	ct := &CalculateTool{}

	result, err := ct.Execute(context.Background(), `{"expr":"1 / 0.00000000000000000000000000000000000000000000000001"}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	// Should be a very large number or +Inf.
	if !strings.Contains(result, "Inf") && !strings.Contains(result, "e+") {
		t.Logf("result: %s", result)
	}
}

func TestCalculateTool_NameDescriptionSchema(t *testing.T) {
	ct := &CalculateTool{}
	if ct.Name() != "calculate" {
		t.Errorf("Name() = %q, want %q", ct.Name(), "calculate")
	}
	if ct.Description() == "" {
		t.Error("Description() is empty")
	}
	schema := ct.Schema()
	var s map[string]any
	if err := json.Unmarshal(schema, &s); err != nil {
		t.Fatalf("Schema() is not valid JSON: %v", err)
	}
}

func TestCalculateTool_ShiftRange(t *testing.T) {
	ct := &CalculateTool{}

	bad := []string{
		`{"expr":"1 << 64"}`,
		`{"expr":"1 << 65"}`,
		`{"expr":"1 >> 64"}`,
		`{"expr":"1 << -1"}`,
	}
	for _, arg := range bad {
		t.Run(arg, func(t *testing.T) {
			_, err := ct.Execute(context.Background(), arg)
			if err == nil {
				t.Errorf("expected error for %s", arg)
			}
		})
	}

	good := []struct {
		expr   string
		expect string
	}{
		{"1 << 63", "-9223372036854775808"},
		{"1 << 0", "1"},
		{"16 >> 0", "16"},
	}
	for _, tt := range good {
		t.Run(tt.expr, func(t *testing.T) {
			result, err := ct.Execute(context.Background(), `{"expr":"`+tt.expr+`"}`)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result != tt.expect {
				t.Errorf("%s = %q, want %q", tt.expr, result, tt.expect)
			}
		})
	}
}

func TestCalculateTool_RightAssoc(t *testing.T) {
	ct := &CalculateTool{}

	tests := []struct {
		expr   string
		expect string
	}{
		{"2 ** 3 ** 2", "512"},
		{"2 ** (3 ** 2)", "512"},
		{"(2 ** 3) ** 2", "64"},
		{"2 ** 2 ** 2 ** 2", "65536"},
		{"-2 ** 2", "-4"},
		{"(-2) ** 2", "4"},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			result, err := ct.Execute(context.Background(), `{"expr":"`+tt.expr+`"}`)
			if err != nil {
				t.Fatalf("Execute() error: %v", err)
			}
			if result != tt.expect {
				t.Errorf("%s = %q, want %q", tt.expr, result, tt.expect)
			}
		})
	}
}

func TestCalculateTool_ChainedOperators(t *testing.T) {
	ct := &CalculateTool{}

	tests := []struct {
		expr   string
		expect string
	}{
		{"1 + 2 + 3 + 4", "10"},
		{"10 - 3 - 2", "5"},
		{"2 * 3 * 4", "24"},
		{"100 / 5 / 2", "10"},
		{"3 & 7 & 15", "3"},
		{"1 | 2 | 4", "7"},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			result, err := ct.Execute(context.Background(), `{"expr":"`+tt.expr+`"}`)
			if err != nil {
				t.Fatalf("Execute() error: %v", err)
			}
			if result != tt.expect {
				t.Errorf("%s = %q, want %q", tt.expr, result, tt.expect)
			}
		})
	}
}

func TestCalculateTool_UnaryPrecedence(t *testing.T) {
	ct := &CalculateTool{}

	tests := []struct {
		expr   string
		expect string
	}{
		{"-2 ** 4", "-16"},
		{"-2 * 3", "-6"},
		{"~1 & 7", "6"},
		{"!0 + 5", "6"},
		{"-2 ** 2 ** 3", "-256"},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			result, err := ct.Execute(context.Background(), `{"expr":"`+tt.expr+`"}`)
			if err != nil {
				t.Fatalf("Execute() error: %v", err)
			}
			if result != tt.expect {
				t.Errorf("%s = %q, want %q", tt.expr, result, tt.expect)
			}
		})
	}
}
