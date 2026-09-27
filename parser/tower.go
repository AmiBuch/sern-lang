package parser

import (
	"slices"

	"sern/ast"
	"sern/lexer"
	"sern/token"
)

// This file is an alternative expression parser: classic recursive
// descent with one function per precedence level (a
// "tower"), lowest at the top. It builds exactly the same trees as the
// Pratt parser in expr/infix; TestTowerAgrees checks that.
//
//	ternary  := or ('?' ternary ':' ternary)?
//	or       := and ('||' and)*
//	and      := equality ('&&' equality)*
//	equality := compare (('==' | '!=') compare)*
//	compare  := sum (('<' | '<=' | '>' | '>=') sum)*
//	sum      := product (('+' | '-') product)*
//	product  := unary (('*' | '/' | '%') unary)*
//	unary    := ('-' | '!') unary | power
//	power    := postfix ('**' unary)?
//	postfix  := primary ('(' args ')' | '[' expr ']' | '.' name)*

// ParseTower parses src using the precedence tower instead of Pratt parsing.
func ParseTower(src string) (*ast.Program, error) {
	return parseWith(lexer.New(src), true)
}

func (p *Parser) ternary() ast.Expr {
	cond := p.or()
	t, ok := p.atOp(token.QUESTION)
	if !ok {
		return cond
	}
	then := p.ternary()
	p.expect(token.COLON, "in 'cond ? a : b'")
	return &ast.CondExpr{P: t.Pos, Cond: cond, Then: then, Else: p.ternary()}
}

func (p *Parser) or() ast.Expr  { return p.leftAssoc(p.and, token.OR) }
func (p *Parser) and() ast.Expr { return p.leftAssoc(p.equality, token.AND) }
func (p *Parser) equality() ast.Expr {
	return p.leftAssoc(p.compare, token.EQ, token.NEQ)
}
func (p *Parser) compare() ast.Expr {
	return p.leftAssoc(p.sum, token.LT, token.LE, token.GT, token.GE)
}
func (p *Parser) sum() ast.Expr { return p.leftAssoc(p.product, token.PLUS, token.MINUS) }
func (p *Parser) product() ast.Expr {
	return p.leftAssoc(p.unary, token.STAR, token.SLASH, token.PERCENT)
}

func (p *Parser) unary() ast.Expr {
	p.nl()
	if t := p.cur(); t.Type == token.MINUS || t.Type == token.BANG {
		p.advance()
		return &ast.UnaryExpr{P: t.Pos, Op: t.Type, X: p.unary()}
	}
	return p.power()
}

func (p *Parser) power() ast.Expr {
	base := p.postfix()
	if t, ok := p.atOp(token.STAR_STAR); ok {
		// The exponent is a unary, not a power, which is what makes '**'
		// right-associative (2 ** 3 ** 2) and allows 2 ** -1.
		return &ast.BinaryExpr{P: t.Pos, Op: t.Type, L: base, R: p.unary()}
	}
	return base
}

func (p *Parser) postfix() ast.Expr {
	x := p.prefix()
	for {
		// A '.' may start the next line (method chains), but a '(' or '['
		// there starts a new statement.
		p.nlThen(func(t token.Token) bool { return t.Type == token.DOT })
		t := p.cur()
		if t.Type != token.DOT && (t.Type != token.LPAREN && t.Type != token.LBRACKET || t.NewlineBefore) {
			return x
		}
		x = p.infix(x, precPostfix) // shared with Pratt: call, index, field
	}
}

// leftAssoc parses next (op next)* and folds the operands to the left.
func (p *Parser) leftAssoc(next func() ast.Expr, ops ...token.Type) ast.Expr {
	left := next()
	for {
		t, ok := p.atOp(ops...)
		if !ok {
			return left
		}
		left = &ast.BinaryExpr{P: t.Pos, Op: t.Type, L: left, R: next()}
	}
}

// atOp consumes the next token if it is one of ops, looking past line
// breaks the same way the Pratt loop does.
func (p *Parser) atOp(ops ...token.Type) (token.Token, bool) {
	p.nlThen(func(t token.Token) bool { return slices.Contains(ops, t.Type) })
	if t := p.cur(); slices.Contains(ops, t.Type) {
		p.advance()
		return t, true
	}
	return token.Token{}, false
}
