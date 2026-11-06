package lookup

import (
	"errors"
	"fmt"
	"strings"
)

const (
	maxWhereLen    = 500
	maxWhereTokens = 120
	maxWhereDepth  = 8
	maxInList      = 50
)

type tokKind int

const (
	tIdent tokKind = iota + 1
	tKeyword
	tString
	tNumber
	tOp
	tLParen
	tRParen
	tComma
)

type token struct {
	kind tokKind
	text string // keyword бол жижиг үсэгтэй
}

var whereKeywords = map[string]bool{"and": true, "or": true, "not": true, "is": true, "null": true, "in": true, "true": true, "false": true}

// ValidateWhere — Spec.Where-ийн аюулгүй дэд олонлог:
//
//	expr    := and (OR and)*
//	and     := unary (AND unary)*
//	unary   := NOT unary | '(' expr ')' | ident cond
//	cond    := (= | <> | != | < | <= | > | >=) literal | IS [NOT] NULL | [NOT] IN '(' literal {, literal} ')'
//	literal := 'мөр' | тоо | TRUE | FALSE
//
// Багана нь жижиг үсэгтэй энгийн нэр. `;`, `--`, `/*`, функц дуудлага, subquery,
// cast, параметр тэмдэг зэргийг ЗӨВШӨӨРӨХГҮЙ.
func ValidateWhere(where string) error {
	if strings.TrimSpace(where) == "" {
		return errors.New("khan/lookup: Where хоосон")
	}
	if len(where) > maxWhereLen {
		return fmt.Errorf("khan/lookup: Where хэт урт (дээд %d)", maxWhereLen)
	}
	for _, bad := range []string{";", "--", "/*", "*/"} {
		if strings.Contains(where, bad) {
			return fmt.Errorf("khan/lookup: Where-д %q зөвшөөрөгдөхгүй", bad)
		}
	}
	toks, err := tokenizeWhere(where)
	if err != nil {
		return err
	}
	p := &whereParser{toks: toks}
	if err := p.expr(0); err != nil {
		return err
	}
	if p.pos != len(toks) {
		return fmt.Errorf("khan/lookup: Where-д илүү тэмдэгт: %q", toks[p.pos].text)
	}
	return nil
}

func tokenizeWhere(s string) ([]token, error) {
	var toks []token
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '(':
			toks = append(toks, token{tLParen, "("})
			i++
		case c == ')':
			toks = append(toks, token{tRParen, ")"})
			i++
		case c == ',':
			toks = append(toks, token{tComma, ","})
			i++
		case c == '\'':
			j := i + 1
			for {
				if j >= len(s) {
					return nil, errors.New("khan/lookup: Where-д хаагдаагүй мөр литерал")
				}
				if s[j] == '\'' {
					if j+1 < len(s) && s[j+1] == '\'' {
						j += 2
						continue
					}
					break
				}
				if s[j] == '\\' || s[j] == 0 || s[j] == '?' {
					return nil, errors.New("khan/lookup: Where-ийн мөр литералд \\ , ? , NUL зөвшөөрөгдөхгүй")
				}
				j++
			}
			toks = append(toks, token{tString, s[i : j+1]})
			i = j + 1
		case c >= '0' && c <= '9' || (c == '-' && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9'):
			j := i + 1
			for j < len(s) && s[j] >= '0' && s[j] <= '9' {
				j++
			}
			if j+1 < len(s) && s[j] == '.' && s[j+1] >= '0' && s[j+1] <= '9' {
				j++
				for j < len(s) && s[j] >= '0' && s[j] <= '9' {
					j++
				}
			}
			if j < len(s) && (isIdentByte(s[j])) {
				return nil, fmt.Errorf("khan/lookup: Where-ийн тоо буруу: %q", s[i:j+1])
			}
			toks = append(toks, token{tNumber, s[i:j]})
			i = j
		case c == '=' || c == '<' || c == '>' || c == '!':
			j := i + 1
			if j < len(s) && (s[j] == '=' || (c == '<' && s[j] == '>')) {
				j++
			}
			op := s[i:j]
			switch op {
			case "=", "<>", "!=", "<", "<=", ">", ">=":
			default:
				return nil, fmt.Errorf("khan/lookup: Where-ийн оператор буруу: %q", op)
			}
			toks = append(toks, token{tOp, op})
			i = j
		case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_':
			j := i
			for j < len(s) && isIdentByte(s[j]) {
				j++
			}
			word := s[i:j]
			if lw := strings.ToLower(word); whereKeywords[lw] {
				toks = append(toks, token{tKeyword, lw})
			} else {
				if word != lw {
					return nil, fmt.Errorf("khan/lookup: Where-ийн багана жижиг үсэгтэй байх ёстой: %q", word)
				}
				toks = append(toks, token{tIdent, word})
			}
			i = j
		default:
			return nil, fmt.Errorf("khan/lookup: Where-д зөвшөөрөгдөөгүй тэмдэгт: %q", string(c))
		}
		if len(toks) > maxWhereTokens {
			return nil, errors.New("khan/lookup: Where хэт төвөгтэй")
		}
	}
	return toks, nil
}

func isIdentByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}

type whereParser struct {
	toks []token
	pos  int
}

func (p *whereParser) peek() (token, bool) {
	if p.pos >= len(p.toks) {
		return token{}, false
	}
	return p.toks[p.pos], true
}

func (p *whereParser) isKW(kw string) bool {
	t, ok := p.peek()
	return ok && t.kind == tKeyword && t.text == kw
}

func (p *whereParser) accept(kind tokKind) bool {
	if t, ok := p.peek(); ok && t.kind == kind {
		p.pos++
		return true
	}
	return false
}

func (p *whereParser) acceptKW(kw string) bool {
	if p.isKW(kw) {
		p.pos++
		return true
	}
	return false
}

var errWhere = errors.New("khan/lookup: Where буруу бүтэцтэй")

func (p *whereParser) expr(depth int) error {
	if depth > maxWhereDepth {
		return errors.New("khan/lookup: Where хэт гүн хаалттай")
	}
	for {
		if err := p.and(depth); err != nil {
			return err
		}
		if !p.acceptKW("or") {
			return nil
		}
	}
}

func (p *whereParser) and(depth int) error {
	for {
		if err := p.unary(depth); err != nil {
			return err
		}
		if !p.acceptKW("and") {
			return nil
		}
	}
}

func (p *whereParser) unary(depth int) error {
	if p.acceptKW("not") {
		if depth+1 > maxWhereDepth {
			return errors.New("khan/lookup: Where хэт гүн")
		}
		return p.unary(depth + 1)
	}
	if p.accept(tLParen) {
		if err := p.expr(depth + 1); err != nil {
			return err
		}
		if !p.accept(tRParen) {
			return errWhere
		}
		return nil
	}
	if !p.accept(tIdent) {
		return errWhere
	}
	return p.cond()
}

func (p *whereParser) cond() error {
	if t, ok := p.peek(); ok && t.kind == tOp {
		p.pos++
		return p.literal()
	}
	if p.acceptKW("is") {
		p.acceptKW("not")
		if !p.acceptKW("null") {
			return errWhere
		}
		return nil
	}
	p.acceptKW("not")
	if !p.acceptKW("in") || !p.accept(tLParen) {
		return errWhere
	}
	n := 0
	for {
		if err := p.literal(); err != nil {
			return err
		}
		n++
		if n > maxInList {
			return errors.New("khan/lookup: Where-ийн IN жагсаалт хэт урт")
		}
		if p.accept(tComma) {
			continue
		}
		if p.accept(tRParen) {
			return nil
		}
		return errWhere
	}
}

func (p *whereParser) literal() error {
	t, ok := p.peek()
	if !ok {
		return errWhere
	}
	switch {
	case t.kind == tString, t.kind == tNumber, t.kind == tKeyword && (t.text == "true" || t.text == "false"):
		p.pos++
		return nil
	}
	return errWhere
}
