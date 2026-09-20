package orchestrator

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// Eval evaluates a condition expression against the run state.
//
// Supported syntax: && || !, parentheses, comparisons (== != > < >= <=),
// the `contains` operator, and property access such as `review.passed`,
// `review.failed`, `outputs.review`, `artifacts.plan`, `iterations`.
func Eval(expr string, st *State, iterations int) (bool, error) {
	if strings.TrimSpace(expr) == "" {
		return true, nil
	}
	p := &parser{tokens: lex(expr), state: st, iterations: iterations}
	v, err := p.parseOr()
	if err != nil {
		return false, err
	}
	if p.peek().kind != tEOF {
		return false, fmt.Errorf("condition %q: unexpected token %q", expr, p.peek().text)
	}
	return v.truthy(), nil
}

type tokKind int

const (
	tEOF tokKind = iota
	tIdent
	tNumber
	tString
	tOp
	tLParen
	tRParen
	tAnd
	tOr
	tNot
	tDot
)

type token struct {
	kind tokKind
	text string
}

func lex(s string) []token {
	var toks []token
	runes := []rune(s)
	for i := 0; i < len(runes); {
		r := runes[i]
		switch {
		case unicode.IsSpace(r):
			i++
		case r == '(':
			toks = append(toks, token{tLParen, "("})
			i++
		case r == ')':
			toks = append(toks, token{tRParen, ")"})
			i++
		case r == '.':
			toks = append(toks, token{tDot, "."})
			i++
		case r == '!':
			if i+1 < len(runes) && runes[i+1] == '=' {
				toks = append(toks, token{tOp, "!="})
				i += 2
			} else {
				toks = append(toks, token{tNot, "!"})
				i++
			}
		case r == '=':
			if i+1 < len(runes) && runes[i+1] == '=' {
				toks = append(toks, token{tOp, "=="})
				i += 2
			} else {
				toks = append(toks, token{tOp, "=="})
				i++
			}
		case r == '>' || r == '<':
			op := string(r)
			if i+1 < len(runes) && runes[i+1] == '=' {
				op += "="
				i += 2
			} else {
				i++
			}
			toks = append(toks, token{tOp, op})
		case r == '&':
			if i+1 < len(runes) && runes[i+1] == '&' {
				toks = append(toks, token{tAnd, "&&"})
				i += 2
			} else {
				i++
			}
		case r == '|':
			if i+1 < len(runes) && runes[i+1] == '|' {
				toks = append(toks, token{tOr, "||"})
				i += 2
			} else {
				i++
			}
		case r == '"' || r == '\'':
			quote := r
			i++
			var sb strings.Builder
			for i < len(runes) && runes[i] != quote {
				sb.WriteRune(runes[i])
				i++
			}
			i++
			toks = append(toks, token{tString, sb.String()})
		case unicode.IsDigit(r):
			start := i
			for i < len(runes) && (unicode.IsDigit(runes[i]) || runes[i] == '.') {
				i++
			}
			toks = append(toks, token{tNumber, string(runes[start:i])})
		case unicode.IsLetter(r) || r == '_':
			start := i
			for i < len(runes) && (unicode.IsLetter(runes[i]) || unicode.IsDigit(runes[i]) || runes[i] == '_') {
				i++
			}
			word := string(runes[start:i])
			if word == "contains" {
				toks = append(toks, token{tOp, "contains"})
			} else {
				toks = append(toks, token{tIdent, word})
			}
		default:
			i++
		}
	}
	toks = append(toks, token{tEOF, ""})
	return toks
}

type value struct {
	s     string
	n     float64
	isNum bool
}

func (v value) truthy() bool {
	if v.isNum {
		return v.n != 0
	}
	t := strings.TrimSpace(strings.ToLower(v.s))
	return t != "" && t != "false" && t != "0" && t != "no"
}

func (v value) asFloat() (float64, bool) {
	if v.isNum {
		return v.n, true
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v.s), 64)
	return f, err == nil
}

type parser struct {
	tokens     []token
	pos        int
	state      *State
	iterations int
}

func (p *parser) peek() token { return p.tokens[p.pos] }
func (p *parser) next() token {
	t := p.tokens[p.pos]
	if p.pos < len(p.tokens)-1 {
		p.pos++
	}
	return t
}

func (p *parser) parseOr() (value, error) {
	left, err := p.parseAnd()
	if err != nil {
		return value{}, err
	}
	for p.peek().kind == tOr {
		p.next()
		right, err := p.parseAnd()
		if err != nil {
			return value{}, err
		}
		left = boolValue(left.truthy() || right.truthy())
	}
	return left, nil
}

func (p *parser) parseAnd() (value, error) {
	left, err := p.parseUnary()
	if err != nil {
		return value{}, err
	}
	for p.peek().kind == tAnd {
		p.next()
		right, err := p.parseUnary()
		if err != nil {
			return value{}, err
		}
		left = boolValue(left.truthy() && right.truthy())
	}
	return left, nil
}

func (p *parser) parseUnary() (value, error) {
	if p.peek().kind == tNot {
		p.next()
		v, err := p.parseUnary()
		if err != nil {
			return value{}, err
		}
		return boolValue(!v.truthy()), nil
	}
	return p.parseComparison()
}

func (p *parser) parseComparison() (value, error) {
	left, err := p.parseValue()
	if err != nil {
		return value{}, err
	}
	if p.peek().kind != tOp {
		return left, nil
	}
	op := p.next().text
	right, err := p.parseValue()
	if err != nil {
		return value{}, err
	}
	switch op {
	case "==":
		return boolValue(valuesEqual(left, right)), nil
	case "!=":
		return boolValue(!valuesEqual(left, right)), nil
	case "contains":
		return boolValue(strings.Contains(left.s, right.s)), nil
	case ">", "<", ">=", "<=":
		lf, lok := left.asFloat()
		rf, rok := right.asFloat()
		if !lok || !rok {
			return value{}, fmt.Errorf("operator %s requires numbers", op)
		}
		switch op {
		case ">":
			return boolValue(lf > rf), nil
		case "<":
			return boolValue(lf < rf), nil
		case ">=":
			return boolValue(lf >= rf), nil
		case "<=":
			return boolValue(lf <= rf), nil
		}
	}
	return value{}, fmt.Errorf("unknown operator %q", op)
}

func (p *parser) parseValue() (value, error) {
	t := p.peek()
	switch t.kind {
	case tNumber:
		p.next()
		f, _ := strconv.ParseFloat(t.text, 64)
		return value{n: f, isNum: true}, nil
	case tString:
		p.next()
		return value{s: t.text}, nil
	case tLParen:
		p.next()
		v, err := p.parseOr()
		if err != nil {
			return value{}, err
		}
		if p.peek().kind != tRParen {
			return value{}, fmt.Errorf("missing closing parenthesis")
		}
		p.next()
		return v, nil
	case tIdent:
		p.next()
		parts := []string{t.text}
		for p.peek().kind == tDot {
			p.next()
			prop := p.peek()
			if prop.kind != tIdent {
				return value{}, fmt.Errorf("expected property name after '.'")
			}
			p.next()
			parts = append(parts, prop.text)
		}
		return p.resolve(parts)
	}
	return value{}, fmt.Errorf("unexpected token %q", t.text)
}

func (p *parser) resolve(parts []string) (value, error) {
	if len(parts) == 1 {
		switch parts[0] {
		case "true":
			return boolValue(true), nil
		case "false":
			return boolValue(false), nil
		case "iterations":
			return value{n: float64(p.iterations), isNum: true}, nil
		}
	}
	var content string
	rest := parts[1:]
	switch parts[0] {
	case "outputs":
		if len(parts) >= 2 {
			content = p.state.Output(parts[1])
			rest = parts[2:]
		}
	case "artifacts":
		if len(parts) >= 2 {
			arts, _ := p.state.Snapshot()
			content = arts[strings.Join(parts[1:], ".")]
			rest = nil
		}
	case "inputs":
		if len(parts) >= 2 {
			content = p.state.Inputs[parts[1]]
			rest = parts[2:]
		}
	default:
		content = p.state.Output(parts[0])
		rest = parts[1:]
	}
	if len(rest) > 0 {
		switch rest[0] {
		case "passed":
			return boolValue(passed(content)), nil
		case "failed":
			return boolValue(failed(content)), nil
		case "empty":
			return boolValue(strings.TrimSpace(content) == ""), nil
		case "nonempty":
			return boolValue(strings.TrimSpace(content) != ""), nil
		}
	}
	return value{s: content}, nil
}

func passed(content string) bool {
	up := strings.ToUpper(content)
	if strings.Contains(up, "FAIL") {
		return false
	}
	return strings.Contains(up, "PASS") || strings.Contains(up, "APPROV")
}

func failed(content string) bool {
	return strings.Contains(strings.ToUpper(content), "FAIL")
}

func boolValue(b bool) value {
	if b {
		return value{s: "true"}
	}
	return value{s: "false"}
}

func valuesEqual(a, b value) bool {
	if af, ok := a.asFloat(); ok {
		if bf, ok := b.asFloat(); ok {
			return af == bf
		}
	}
	return a.s == b.s
}
