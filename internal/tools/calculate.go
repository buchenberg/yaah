package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"

	"github.com/buchenberg/yaah/internal/prompts"
)

// CalculateTool evaluates mathematical expressions with zero external dependencies.
// Supports arithmetic, bitwise ops, trigonometry, logarithms, and hex/binary/octal
// literals — all the math agents get wrong when guessing.
type CalculateTool struct{}

func (t *CalculateTool) Name() string { return "calculate" }
func (t *CalculateTool) Description() string {
	return prompts.ToolDescription("calculate")
}

func (t *CalculateTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"expr": {
				"type": "string",
				"description": "Mathematical expression to evaluate. Supports: + - * / % **, bitwise & | ^ << >> ~, functions sqrt/abs/ceil/floor/round/log/log10/log2/exp/sin/cos/tan/asin/acos/atan/min/max, constants pi/e, hex (0xFF), binary (0b1010), octal (0o777)"
			}
		},
		"required": ["expr"]
	}`)
}

func (t *CalculateTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Expr string `json:"expr"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("calculate: invalid arguments: %w", err)
	}
	if params.Expr == "" {
		return "", fmt.Errorf("calculate: expr is required")
	}

	result, err := evalExpr(params.Expr)
	if err != nil {
		return "", fmt.Errorf("calculate: %w", err)
	}

	if math.IsNaN(result) {
		return "NaN", nil
	}
	if math.IsInf(result, 1) {
		return "+Inf", nil
	}
	if math.IsInf(result, -1) {
		return "-Inf", nil
	}
	if result == math.Trunc(result) {
		return fmt.Sprintf("%.0f", result), nil
	}
	return strconv.FormatFloat(result, 'g', -1, 64), nil
}

// ---------------------------------------------------------------------------
// Pratt parser
// ---------------------------------------------------------------------------

type calcTokKind int

const (
	ctkEOF calcTokKind = iota
	ctkNum
	ctkIdent
	ctkPlus
	ctkMinus
	ctkStar
	ctkSlash
	ctkPercent
	ctkCaret   // ^ (xor)
	ctkAmp     // &
	ctkPipeVal // |
	ctkLShift  // <<
	ctkRShift  // >>
	ctkBang    // !
	ctkTilde   // ~
	ctkLParen
	ctkRParen
	ctkComma
	ctkStarStar // **
	ctkInvalid  // unrecognized character
)

type calcTok struct {
	kind calcTokKind
	text string
}

type calcLexer struct {
	src []rune
	pos int
}

func (l *calcLexer) next() calcTok {
	l.skipWS()
	if l.pos >= len(l.src) {
		return calcTok{ctkEOF, ""}
	}

	ch := l.src[l.pos]

	// Number (including leading decimal like .5).
	if (ch == '.' && l.pos+1 < len(l.src) && unicode.IsDigit(l.src[l.pos+1])) || unicode.IsDigit(ch) {
		return l.scanNumber()
	}

	// Identifier / keyword.
	if unicode.IsLetter(ch) || ch == '_' {
		return l.scanIdent()
	}

	// Multi-char operators.
	if ch == '*' && l.pos+1 < len(l.src) && l.src[l.pos+1] == '*' {
		l.pos += 2
		return calcTok{ctkStarStar, "**"}
	}
	if ch == '<' && l.pos+1 < len(l.src) && l.src[l.pos+1] == '<' {
		l.pos += 2
		return calcTok{ctkLShift, "<<"}
	}
	if ch == '>' && l.pos+1 < len(l.src) && l.src[l.pos+1] == '>' {
		l.pos += 2
		return calcTok{ctkRShift, ">>"}
	}

	l.pos++
	switch ch {
	case '+':
		return calcTok{ctkPlus, "+"}
	case '-':
		return calcTok{ctkMinus, "-"}
	case '*':
		return calcTok{ctkStar, "*"}
	case '/':
		return calcTok{ctkSlash, "/"}
	case '%':
		return calcTok{ctkPercent, "%"}
	case '^':
		return calcTok{ctkCaret, "^"}
	case '&':
		return calcTok{ctkAmp, "&"}
	case '|':
		return calcTok{ctkPipeVal, "|"}
	case '!':
		return calcTok{ctkBang, "!"}
	case '~':
		return calcTok{ctkTilde, "~"}
	case '(':
		return calcTok{ctkLParen, "("}
	case ')':
		return calcTok{ctkRParen, ")"}
	case ',':
		return calcTok{ctkComma, ","}
	}
	return calcTok{ctkInvalid, string(ch)}
}

func (l *calcLexer) skipWS() {
	for l.pos < len(l.src) && unicode.IsSpace(l.src[l.pos]) {
		l.pos++
	}
}

func (l *calcLexer) scanNumber() calcTok {
	start := l.pos

	if l.src[l.pos] == '0' && l.pos+1 < len(l.src) {
		switch l.src[l.pos+1] {
		case 'x', 'X':
			l.pos += 2
			for l.pos < len(l.src) && isHexDigit(l.src[l.pos]) {
				l.pos++
			}
			return calcTok{ctkNum, string(l.src[start:l.pos])}
		case 'b', 'B':
			l.pos += 2
			for l.pos < len(l.src) && (l.src[l.pos] == '0' || l.src[l.pos] == '1') {
				l.pos++
			}
			return calcTok{ctkNum, string(l.src[start:l.pos])}
		case 'o', 'O':
			l.pos += 2
			for l.pos < len(l.src) && l.src[l.pos] >= '0' && l.src[l.pos] <= '7' {
				l.pos++
			}
			return calcTok{ctkNum, string(l.src[start:l.pos])}
		}
	}

	if l.src[l.pos] == '.' {
		l.pos++
	}
	for l.pos < len(l.src) && unicode.IsDigit(l.src[l.pos]) {
		l.pos++
	}
	if l.pos < len(l.src) && l.src[l.pos] == '.' {
		l.pos++
		for l.pos < len(l.src) && unicode.IsDigit(l.src[l.pos]) {
			l.pos++
		}
	}
	if l.pos < len(l.src) && (l.src[l.pos] == 'e' || l.src[l.pos] == 'E') {
		l.pos++
		if l.pos < len(l.src) && (l.src[l.pos] == '+' || l.src[l.pos] == '-') {
			l.pos++
		}
		for l.pos < len(l.src) && unicode.IsDigit(l.src[l.pos]) {
			l.pos++
		}
	}
	return calcTok{ctkNum, string(l.src[start:l.pos])}
}

func (l *calcLexer) scanIdent() calcTok {
	start := l.pos
	for l.pos < len(l.src) && (unicode.IsLetter(l.src[l.pos]) || unicode.IsDigit(l.src[l.pos]) || l.src[l.pos] == '_') {
		l.pos++
	}
	return calcTok{ctkIdent, string(l.src[start:l.pos])}
}

func isHexDigit(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}

// --- Parser ---

type calcParser struct {
	lex       *calcLexer
	tok       calcTok
	peek      calcTok
	lastIdent string
}

func newCalcParser(src string) *calcParser {
	p := &calcParser{lex: &calcLexer{src: []rune(src)}}
	p.tok = p.lex.next()
	p.peek = p.lex.next()
	return p
}

func (p *calcParser) advance() {
	p.tok = p.peek
	p.peek = p.lex.next()
}

func (p *calcParser) prec() (precedence int, rightAssoc bool) {
	switch p.tok.kind {
	case ctkPipeVal:
		return 1, false
	case ctkCaret:
		return 2, false
	case ctkAmp:
		return 3, false
	case ctkLShift, ctkRShift:
		return 4, false
	case ctkPlus, ctkMinus:
		return 5, false
	case ctkStar, ctkSlash, ctkPercent:
		return 6, false
	case ctkStarStar:
		return 8, true
	}
	return 0, false
}

func (p *calcParser) parseExpr(minPrec int) (float64, error) {
	var left float64

	switch p.tok.kind {
	case ctkNum:
		var err error
		left, err = parseNumber(p.tok.text)
		if err != nil {
			return 0, err
		}
		p.advance()

	case ctkIdent:
		name := p.tok.text
		p.advance()
		if p.tok.kind == ctkLParen {
			p.lastIdent = name
		} else {
			v := resolveConstant(name)
			if math.IsNaN(v) {
				return 0, fmt.Errorf("unknown constant %q — did you mean to call %s()?", name, name)
			}
			left = v
		}

	case ctkMinus, ctkPlus, ctkBang, ctkTilde:
		op := p.tok.kind
		p.advance()
		right, err := p.parseExpr(7)
		if err != nil {
			return 0, err
		}
		left, err = applyUnary(op, right)
		if err != nil {
			return 0, err
		}

	case ctkLParen:
		p.advance()
		var err error
		left, err = p.parseExpr(0)
		if err != nil {
			return 0, err
		}
		if p.tok.kind != ctkRParen {
			return 0, fmt.Errorf("expected )")
		}
		p.advance()

	case ctkInvalid:
		return 0, fmt.Errorf("invalid character %q", p.tok.text)

	default:
		return 0, fmt.Errorf("unexpected token %q", p.tok.text)
	}

	// Infix operators and function calls.
	for {
		// Function call: ident already consumed, tok is (.
		if p.tok.kind == ctkLParen && p.lastIdent != "" {
			var err error
			left, err = p.parseCall()
			if err != nil {
				return 0, err
			}
			continue
		}

		prec, rightAssoc := p.prec()
		if prec == 0 || prec < minPrec {
			break
		}

		op := p.tok.kind
		p.advance()
		nextMinPrec := prec + 1
		if rightAssoc {
			nextMinPrec = prec
		}
		right, err := p.parseExpr(nextMinPrec)
		if err != nil {
			return 0, err
		}
		left, err = applyBinary(op, left, right)
		if err != nil {
			return 0, err
		}
	}

	return left, nil
}

func (p *calcParser) parseCall() (float64, error) {
	name := p.lastIdent
	p.lastIdent = ""
	p.advance() // consume (

	var args []float64
	if p.tok.kind != ctkRParen {
		for {
			v, err := p.parseExpr(0)
			if err != nil {
				return 0, err
			}
			args = append(args, v)
			if p.tok.kind == ctkRParen {
				break
			}
			if p.tok.kind != ctkComma {
				return 0, fmt.Errorf("expected , or ) in function call, got %q", p.tok.text)
			}
			p.advance()
		}
	}
	p.advance() // consume )

	return callFunc(name, args)
}

// ---------------------------------------------------------------------------
// Evaluation helpers
// ---------------------------------------------------------------------------

func parseNumber(s string) (float64, error) {
	if len(s) >= 2 && s[0] == '0' {
		switch s[1] {
		case 'x', 'X':
			v, err := strconv.ParseInt(s[2:], 16, 64)
			return float64(v), err
		case 'b', 'B':
			v, err := strconv.ParseInt(s[2:], 2, 64)
			return float64(v), err
		case 'o', 'O':
			v, err := strconv.ParseInt(s[2:], 8, 64)
			return float64(v), err
		}
	}
	return strconv.ParseFloat(s, 64)
}

func resolveConstant(name string) float64 {
	switch strings.ToLower(name) {
	case "pi", "π":
		return math.Pi
	case "e":
		return math.E
	}
	return math.NaN()
}

func applyUnary(op calcTokKind, v float64) (float64, error) {
	switch op {
	case ctkMinus:
		return -v, nil
	case ctkPlus:
		return v, nil
	case ctkBang:
		if v == 0 {
			return 1, nil
		}
		return 0, nil
	case ctkTilde:
		i, err := toInt64(v, "~ operand")
		if err != nil {
			return 0, err
		}
		return float64(^i), nil
	}
	return v, nil
}

// int64ConvBounds are the exclusive float bounds of the int64 range:
// values below -2^63 or at/above 2^63 do not survive the conversion.
const (
	int64MinFloat = -9223372036854775808.0
	int64MaxFloat = 9223372036854775808.0
)

// toInt64 converts a float operand for bitwise/shift use, rejecting the
// NaN/±Inf/out-of-range values whose int64 conversion is implementation
// defined per the Go spec.
func toInt64(v float64, label string) (int64, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("%s is not a finite number: %g", label, v)
	}
	if v < int64MinFloat || v >= int64MaxFloat {
		return 0, fmt.Errorf("%s out of int64 range: %g", label, v)
	}
	return int64(v), nil
}

func applyBinary(op calcTokKind, a, b float64) (float64, error) {
	switch op {
	case ctkPlus:
		return a + b, nil
	case ctkMinus:
		return a - b, nil
	case ctkStar:
		return a * b, nil
	case ctkSlash:
		if b == 0 {
			return 0, fmt.Errorf("division by zero")
		}
		return a / b, nil
	case ctkPercent:
		if b == 0 {
			return 0, fmt.Errorf("modulo by zero")
		}
		return math.Mod(a, b), nil
	case ctkStarStar:
		return math.Pow(a, b), nil
	case ctkAmp:
		ia, err := toInt64(a, "left operand")
		if err != nil {
			return 0, err
		}
		ib, err := toInt64(b, "right operand")
		if err != nil {
			return 0, err
		}
		return float64(ia & ib), nil
	case ctkPipeVal:
		ia, err := toInt64(a, "left operand")
		if err != nil {
			return 0, err
		}
		ib, err := toInt64(b, "right operand")
		if err != nil {
			return 0, err
		}
		return float64(ia | ib), nil
	case ctkCaret:
		ia, err := toInt64(a, "left operand")
		if err != nil {
			return 0, err
		}
		ib, err := toInt64(b, "right operand")
		if err != nil {
			return 0, err
		}
		return float64(ia ^ ib), nil
	case ctkLShift:
		ia, err := toInt64(a, "shifted value")
		if err != nil {
			return 0, err
		}
		n := int64(b)
		if n < 0 || n >= 64 {
			return 0, fmt.Errorf("shift count out of range: %g", b)
		}
		return float64(ia << uint64(n)), nil
	case ctkRShift:
		ia, err := toInt64(a, "shifted value")
		if err != nil {
			return 0, err
		}
		n := int64(b)
		if n < 0 || n >= 64 {
			return 0, fmt.Errorf("shift count out of range: %g", b)
		}
		return float64(ia >> uint64(n)), nil
	}
	return 0, fmt.Errorf("calculate: unknown operator (kind %d) — this is a bug", op)
}

type calcBuiltin func([]float64) (float64, error)

var calcBuiltins = map[string]calcBuiltin{
	"sqrt":  calcArity1(math.Sqrt),
	"abs":   calcArity1(math.Abs),
	"ceil":  calcArity1(math.Ceil),
	"floor": calcArity1(math.Floor),
	"round": calcArity1(math.Round),
	"log":   calcArity1(math.Log),
	"log10": calcArity1(math.Log10),
	"log2":  calcArity1(math.Log2),
	"exp":   calcArity1(math.Exp),
	"sin":   calcArity1(math.Sin),
	"cos":   calcArity1(math.Cos),
	"tan":   calcArity1(math.Tan),
	"asin":  calcArity1(math.Asin),
	"acos":  calcArity1(math.Acos),
	"atan":  calcArity1(math.Atan),
	"min":   calcVariadic(math.Min),
	"max":   calcVariadic(math.Max),
	"pow":   calcArity2(math.Pow),
}

func callFunc(name string, args []float64) (float64, error) {
	fn, ok := calcBuiltins[strings.ToLower(name)]
	if !ok {
		return 0, fmt.Errorf("unknown function %q", name)
	}
	return fn(args)
}

func calcArity1(fn func(float64) float64) calcBuiltin {
	return func(args []float64) (float64, error) {
		if len(args) != 1 {
			return 0, fmt.Errorf("expected 1 argument, got %d", len(args))
		}
		return fn(args[0]), nil
	}
}

func calcArity2(fn func(float64, float64) float64) calcBuiltin {
	return func(args []float64) (float64, error) {
		if len(args) != 2 {
			return 0, fmt.Errorf("expected 2 arguments, got %d", len(args))
		}
		return fn(args[0], args[1]), nil
	}
}

func calcVariadic(fn func(float64, float64) float64) calcBuiltin {
	return func(args []float64) (float64, error) {
		if len(args) < 2 {
			return 0, fmt.Errorf("expected at least 2 arguments, got %d", len(args))
		}
		result := args[0]
		for _, v := range args[1:] {
			result = fn(result, v)
		}
		return result, nil
	}
}

func evalExpr(expr string) (float64, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return 0, fmt.Errorf("empty expression")
	}
	p := newCalcParser(expr)
	val, err := p.parseExpr(0)
	if err != nil {
		return 0, err
	}
	if p.tok.kind == ctkInvalid {
		return 0, fmt.Errorf("invalid character %q", p.tok.text)
	}
	if p.tok.kind != ctkEOF {
		return 0, fmt.Errorf("unexpected token %q after expression", p.tok.text)
	}
	return val, nil
}
