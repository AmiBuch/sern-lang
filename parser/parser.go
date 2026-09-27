// Package parser builds a Sern syntax tree using recursive descent for
// statements and Pratt (precedence-climbing) parsing for expressions.
package parser

import (
	"fmt"
	"strconv"
	"strings"

	"sern/ast"
	"sern/lexer"
	"sern/token"
)

// Error is a syntax error. AtEOF is set when the parser ran out of input,
// which the REPL uses to ask for another line instead of failing.
type Error struct {
	Pos   token.Pos
	Msg   string
	AtEOF bool
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Pos, e.Msg) }

// ErrorList is every syntax error found in a file, in source order. Parse
// always returns one (never a bare *Error) when parsing fails.
type ErrorList []*Error

func (l ErrorList) Error() string {
	msgs := make([]string, len(l))
	for i, e := range l {
		msgs[i] = e.Error()
	}
	return strings.Join(msgs, "\n")
}

// maxErrors stops reporting once the error count suggests the parser has
// lost track of the program and further messages would be noise.
const maxErrors = 10

type bailout struct{ err *Error }

type stopParsing struct{}

// Parser holds the token stream for one source file.
type Parser struct {
	toks  []token.Token
	i     int
	errs  ErrorList
	tower bool
	inArm int // > 0 while parsing a match arm's body
}

// Parse lexes and parses a complete program.
func Parse(src string) (*ast.Program, error) { return parseWith(lexer.New(src), false) }

// ParseNewlineTokens parses with the alternative lexer design: explicit
// NEWLINE tokens instead of the NewlineBefore flag.
// It produces the same tree as Parse. The parser serves both designs: the
// newline helpers below (nl, lineEnd, nlThen) are no-ops when the lexer
// never emits NEWLINE.
func ParseNewlineTokens(src string) (*ast.Program, error) {
	return parseWith(lexer.NewNewlineTokens(src), false)
}

// parseWith parses the tokens from l. tower selects the precedence-tower
// expression parser (tower.go) instead of Pratt parsing.
func parseWith(l *lexer.Lexer, tower bool) (prog *ast.Program, err error) {
	toks, lerr := l.All()
	if lerr != nil {
		le := lerr.(*lexer.Error)
		return nil, ErrorList{{Pos: le.Pos, Msg: le.Msg}}
	}
	p := &Parser{toks: toks, tower: tower}
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(stopParsing); !ok {
				panic(r)
			}
			prog, err = nil, p.errs
		}
	}()
	prog = &ast.Program{}
	for !p.check(token.EOF) {
		if s := p.recoverStmt(); s != nil {
			prog.Stmts = append(prog.Stmts, s)
		}
	}
	if len(p.errs) > 0 {
		return nil, p.errs
	}
	return prog, nil
}

// ---- error recovery ----

// recoverStmt parses one statement. On a syntax error it records the error,
// skips ahead to a likely statement boundary (panic-mode recovery), and
// returns nil so the caller can carry on and find more errors.
func (p *Parser) recoverStmt() (s ast.Stmt) {
	start := p.i
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		b, ok := r.(bailout)
		if !ok {
			panic(r)
		}
		// One error per line: a second error on the same line is almost
		// always a consequence of the first (Go's parser does the same).
		if n := len(p.errs); n == 0 || p.errs[n-1].Pos.Line != b.err.Pos.Line {
			p.errs = append(p.errs, b.err)
		}
		if b.err.AtEOF || len(p.errs) >= maxErrors {
			panic(stopParsing{})
		}
		p.sync()
		if p.i == start && !p.check(token.EOF) {
			p.advance() // guarantee progress
		}
		s = nil
	}()
	return p.statement()
}

// sync skips tokens until the start of what is probably the next
// statement: just after a ';', at a line break, at a keyword that begins a
// statement, or at a '}' that closes the enclosing block.
func (p *Parser) sync() {
	for {
		t := p.cur()
		switch t.Type {
		case token.EOF, token.RBRACE:
			return
		case token.SEMICOLON:
			p.advance()
			return
		case token.NEWLINE:
			p.nl()
			return
		case token.LABMEM, token.OPERATION, token.KONGROO, token.IF, token.TIMELEAP,
			token.READING, token.BREAK, token.CONTINUE, token.CONST, token.MATCH:
			return
		}
		if t.NewlineBefore {
			return // recoverStmt guarantees progress if nothing was consumed
		}
		p.advance()
	}
}

// ---- token helpers ----

func (p *Parser) cur() token.Token { return p.toks[p.i] }

// peek returns the token after cur, looking past NEWLINE tokens.
func (p *Parser) peek() token.Token {
	j := p.i + 1
	for j < len(p.toks)-1 && p.toks[j].Type == token.NEWLINE {
		j++
	}
	return p.toks[min(j, len(p.toks)-1)]
}

// ---- newline handling ----
//
// Newlines matter in exactly three places: at the end of a statement
// (endStmt, kongroo), and before a '(' or '[' that would otherwise call or
// index the previous line's value. Everywhere else they are skipped.

// nl skips NEWLINE tokens.
func (p *Parser) nl() {
	for p.toks[p.i].Type == token.NEWLINE {
		p.i++
	}
}

// lineEnd reports whether a line break comes before the next real token.
func (p *Parser) lineEnd() bool {
	return p.cur().Type == token.NEWLINE || p.cur().NewlineBefore
}

// nlThen skips NEWLINE tokens only if the first token after them satisfies
// ok, so that e.g. an operator at the start of the next line continues the
// expression, the way it does with the flag design.
func (p *Parser) nlThen(ok func(token.Token) bool) {
	j := p.i
	for p.toks[j].Type == token.NEWLINE {
		j++
	}
	if j != p.i && ok(p.toks[j]) {
		p.i = j
	}
}

func (p *Parser) advance() token.Token {
	t := p.toks[p.i]
	if p.i < len(p.toks)-1 {
		p.i++
	}
	return t
}

func (p *Parser) check(t token.Type) bool {
	p.nl()
	return p.cur().Type == t
}

func (p *Parser) match(t token.Type) bool {
	if p.check(t) {
		p.advance()
		return true
	}
	return false
}

func (p *Parser) failAt(t token.Token, format string, args ...any) {
	panic(bailout{&Error{Pos: t.Pos, Msg: fmt.Sprintf(format, args...), AtEOF: t.Type == token.EOF}})
}

func (p *Parser) expect(t token.Type, context string) token.Token {
	if !p.check(t) {
		p.failAt(p.cur(), "expected '%s' %s, found %s", t, context, describe(p.cur()))
	}
	return p.advance()
}

func describe(t token.Token) string {
	switch t.Type {
	case token.EOF:
		return "end of file"
	case token.IDENT:
		return fmt.Sprintf("identifier '%s'", t.Lit)
	case token.INT, token.FLOAT:
		return "number " + t.Lit
	case token.STRING, token.STRING_PART, token.STRING_END:
		return fmt.Sprintf("string %q", t.Lit)
	}
	if t.Type.IsKeyword() {
		return fmt.Sprintf("keyword '%s'", t.Type)
	}
	return fmt.Sprintf("'%s'", t.Type)
}

// endStmt enforces that a simple statement is terminated by ';', a newline,
// a closing brace, or the end of the file.
func (p *Parser) endStmt() {
	if p.cur().Type == token.NEWLINE {
		p.match(token.SEMICOLON) // "x\n;" ends x with the ';', as with the flag
		return
	}
	if p.match(token.SEMICOLON) {
		return
	}
	t := p.cur()
	if t.Type == token.RBRACE || t.Type == token.EOF || t.NewlineBefore {
		return
	}
	if t.Type == token.COMMA && p.inArm > 0 {
		return // "1 => print(x), 2 => ..." (matchStmt consumes the comma)
	}
	p.failAt(t, "expected ';' or a newline after statement, found %s", describe(t))
}

// ---- statements ----

func (p *Parser) statement() ast.Stmt {
	t := p.cur()
	switch t.Type {
	case token.LABMEM:
		p.advance()
		name := p.expect(token.IDENT, "after 'labmem'").Lit
		p.expect(token.ASSIGN, fmt.Sprintf("after 'labmem %s' (every lab member needs an initial value)", name))
		val := p.expr(precLowest)
		p.endStmt()
		return &ast.LabmemStmt{P: t.Pos, Name: name, Value: val}

	case token.OPERATION:
		if p.peek().Type == token.IDENT {
			p.advance()
			p.nl()
			name := p.advance().Lit
			fn := p.funcRest(t.Pos, name)
			p.match(token.SEMICOLON)
			return &ast.OperationStmt{P: t.Pos, Fn: fn}
		}

	case token.KONGROO:
		p.advance()
		var val ast.Expr
		if n := p.cur(); !(n.Type == token.SEMICOLON || n.Type == token.RBRACE || n.Type == token.EOF || p.lineEnd() ||
			n.Type == token.COMMA && p.inArm > 0) {
			val = p.expr(precLowest)
		}
		p.endStmt()
		return &ast.KongrooStmt{P: t.Pos, Value: val}

	case token.IF:
		return p.ifStmt()

	case token.CONST:
		p.advance()
		name := p.expect(token.IDENT, "after 'const'").Lit
		p.expect(token.ASSIGN, fmt.Sprintf("after 'const %s'", name))
		val := p.expr(precLowest)
		p.endStmt()
		return &ast.ConstStmt{P: t.Pos, Name: name, Value: val}

	case token.MATCH:
		return p.matchStmt()

	case token.TIMELEAP:
		p.advance()
		cond := p.expr(precLowest)
		body := p.block("after timeleap condition")
		p.match(token.SEMICOLON)
		return &ast.TimeleapStmt{P: t.Pos, Cond: cond, Body: body}

	case token.READING:
		p.advance()
		first := p.expect(token.IDENT, "after 'reading'").Lit
		second := ""
		if p.match(token.COMMA) {
			second = p.expect(token.IDENT, "after ',' in reading").Lit
			if second == first {
				p.failAt(p.toks[p.i-1], "reading binds '%s' twice", first)
			}
		}
		p.expect(token.IN, "in reading loop")
		iter := p.expr(precLowest)
		body := p.block("after reading iterable")
		p.match(token.SEMICOLON)
		return &ast.ReadingStmt{P: t.Pos, First: first, Second: second, Iter: iter, Body: body}

	case token.BREAK:
		p.advance()
		p.endStmt()
		return &ast.BreakStmt{P: t.Pos}

	case token.CONTINUE:
		p.advance()
		p.endStmt()
		return &ast.ContinueStmt{P: t.Pos}

	case token.LBRACE:
		return p.block("")
	}

	// expression statement or assignment
	x := p.expr(precLowest)
	p.nlThen(func(t token.Token) bool { return isAssignOp(t.Type) })
	switch op := p.cur().Type; op {
	case token.ASSIGN, token.PLUS_ASSIGN, token.MINUS_ASSIGN, token.STAR_ASSIGN, token.SLASH_ASSIGN, token.PERCENT_ASSIGN:
		opTok := p.advance()
		switch x.(type) {
		case *ast.Ident, *ast.IndexExpr, *ast.FieldExpr:
		default:
			p.failAt(opTok, "cannot assign to %s", ast.ExprString(x))
		}
		val := p.expr(precLowest)
		p.endStmt()
		return &ast.AssignStmt{P: opTok.Pos, Target: x, Op: op, Value: val}
	}
	p.endStmt()
	return &ast.ExprStmt{P: t.Pos, X: x}
}

func isAssignOp(t token.Type) bool {
	switch t {
	case token.ASSIGN, token.PLUS_ASSIGN, token.MINUS_ASSIGN, token.STAR_ASSIGN, token.SLASH_ASSIGN, token.PERCENT_ASSIGN:
		return true
	}
	return false
}

// matchStmt parses match subject { pattern => stmt, ..., _ => stmt }.
// Arms are separated by commas or newlines; a body is one statement
// (often a block).
func (p *Parser) matchStmt() ast.Stmt {
	t := p.expect(token.MATCH, "")
	m := &ast.MatchStmt{P: t.Pos, Subject: p.expr(precLowest)}
	open := p.expect(token.LBRACE, "after match subject")
	for !p.check(token.RBRACE) {
		if p.check(token.EOF) {
			p.failAt(p.cur(), "unclosed '{' opened at %s", open.Pos)
		}
		arm := ast.MatchArm{P: p.cur().Pos}
		if c := p.cur(); c.Type == token.IDENT && c.Lit == "_" {
			p.advance()
		} else {
			arm.Pattern = p.expr(precLowest)
		}
		p.expect(token.FAT_ARROW, "after match pattern")
		arm.Body = p.armBody()
		m.Arms = append(m.Arms, arm)
		p.match(token.COMMA)
	}
	p.advance()
	p.match(token.SEMICOLON)
	return m
}

// armBody parses a match arm's statement. A simple statement ends at a
// ',' as well as the usual ';', newline or '}'.
func (p *Parser) armBody() ast.Stmt {
	if p.check(token.LBRACE) {
		return p.block("")
	}
	p.inArm++
	defer func() { p.inArm-- }()
	return p.statement()
}

func (p *Parser) ifStmt() ast.Stmt {
	t := p.expect(token.IF, "")
	cond := p.expr(precLowest)
	then := p.block("after if condition")
	s := &ast.IfStmt{P: t.Pos, Cond: cond, Then: then}
	if p.match(token.ELSE) {
		if p.check(token.IF) {
			s.Else = p.ifStmt()
		} else {
			s.Else = p.block("after 'else'")
		}
	}
	p.match(token.SEMICOLON)
	return s
}

func (p *Parser) block(context string) *ast.BlockStmt {
	if context == "" {
		context = "to open block"
	}
	open := p.expect(token.LBRACE, context)
	b := &ast.BlockStmt{P: open.Pos}
	defer func(arm int) { p.inArm = arm }(p.inArm)
	p.inArm = 0 // a ',' inside braces never ends a match arm
	for !p.check(token.RBRACE) {
		if p.check(token.EOF) {
			p.failAt(p.cur(), "unclosed '{' opened at %s", open.Pos)
		}
		if s := p.recoverStmt(); s != nil {
			b.Stmts = append(b.Stmts, s)
		}
	}
	p.advance()
	return b
}

// funcRest parses "(params) { body }" after 'operation [name]'.
func (p *Parser) funcRest(pos token.Pos, name string) *ast.FuncLit {
	p.expect(token.LPAREN, "to start operation parameters")
	var params []string
	seen := map[string]bool{}
	for !p.check(token.RPAREN) {
		pt := p.expect(token.IDENT, "as parameter name")
		if seen[pt.Lit] {
			p.failAt(pt, "duplicate parameter '%s'", pt.Lit)
		}
		seen[pt.Lit] = true
		params = append(params, pt.Lit)
		if !p.match(token.COMMA) {
			break
		}
	}
	p.expect(token.RPAREN, "to close operation parameters")
	if len(params) > 255 {
		p.failAt(p.cur(), "an operation cannot take more than 255 parameters")
	}
	body := p.block("to start operation body")
	return &ast.FuncLit{P: pos, Name: name, Params: params, Body: body}
}

// ---- expressions (Pratt) ----

const (
	precLowest = iota
	precTernary
	precOr
	precAnd
	precEquality
	precCompare
	precSum
	precProduct
	precPrefix
	precPow // above unary minus: -2 ** 2 is -(2 ** 2), as in Python
	precPostfix
)

func infixPrec(t token.Type) int {
	switch t {
	case token.QUESTION:
		return precTernary
	case token.OR:
		return precOr
	case token.AND:
		return precAnd
	case token.EQ, token.NEQ:
		return precEquality
	case token.LT, token.LE, token.GT, token.GE:
		return precCompare
	case token.PLUS, token.MINUS:
		return precSum
	case token.STAR, token.SLASH, token.PERCENT:
		return precProduct
	case token.STAR_STAR:
		return precPow
	case token.LPAREN, token.LBRACKET, token.DOT:
		return precPostfix
	}
	return precLowest
}

func (p *Parser) expr(minPrec int) ast.Expr {
	if p.tower {
		// In tower mode, only full expressions come through here (from
		// statements, parentheses, arguments, ...); the tower handles
		// every operator level itself.
		if minPrec != precLowest {
			panic("internal: tower parser asked for a sub-precedence expression")
		}
		return p.ternary()
	}
	left := p.prefix()
	for {
		p.nlThen(func(t token.Token) bool {
			return infixPrec(t.Type) != precLowest && t.Type != token.LPAREN && t.Type != token.LBRACKET
		})
		t := p.cur()
		prec := infixPrec(t.Type)
		if prec == precLowest || prec <= minPrec {
			return left
		}
		// A '(' or '[' on a new line starts a new statement rather than
		// calling/indexing the previous line's value.
		if (t.Type == token.LPAREN || t.Type == token.LBRACKET) && t.NewlineBefore {
			return left
		}
		left = p.infix(left, prec)
	}
}

func (p *Parser) prefix() ast.Expr {
	p.nl()
	t := p.advance()
	switch t.Type {
	case token.IDENT:
		return &ast.Ident{P: t.Pos, Name: t.Lit}
	case token.INT:
		digits, base := t.Lit, 10
		switch {
		case strings.HasPrefix(digits, "0x"):
			digits, base = digits[2:], 16
		case strings.HasPrefix(digits, "0b"):
			digits, base = digits[2:], 2
		}
		v, err := strconv.ParseInt(digits, base, 64)
		if err != nil {
			p.failAt(t, "integer literal %s does not fit in 64 bits", t.Lit)
		}
		return &ast.IntLit{P: t.Pos, Value: v}
	case token.FLOAT:
		v, err := strconv.ParseFloat(t.Lit, 64)
		if err != nil {
			p.failAt(t, "invalid float literal %s", t.Lit)
		}
		return &ast.FloatLit{P: t.Pos, Value: v}
	case token.STRING:
		return &ast.StringLit{P: t.Pos, Value: t.Lit}
	case token.STRING_PART:
		return p.interpolation(t)
	case token.TRUE, token.FALSE:
		return &ast.BoolLit{P: t.Pos, Value: t.Type == token.TRUE}
	case token.NIL:
		return &ast.NilLit{P: t.Pos}
	case token.MINUS, token.BANG:
		x := p.expr(precPrefix)
		return &ast.UnaryExpr{P: t.Pos, Op: t.Type, X: x}
	case token.LPAREN:
		x := p.expr(precLowest)
		p.expect(token.RPAREN, "to close '('")
		return x
	case token.LBRACKET:
		lst := &ast.ListLit{P: t.Pos}
		for !p.check(token.RBRACKET) {
			lst.Elems = append(lst.Elems, p.expr(precLowest))
			if !p.match(token.COMMA) {
				break
			}
		}
		p.expect(token.RBRACKET, "to close list")
		return lst
	case token.LBRACE:
		m := &ast.MapLit{P: t.Pos}
		for !p.check(token.RBRACE) {
			var key ast.Expr
			// Bare identifiers before ':' are string keys: {nodes: 5}
			if k := p.cur(); k.Type == token.IDENT && p.peek().Type == token.COLON {
				p.advance()
				key = &ast.StringLit{P: k.Pos, Value: k.Lit}
			} else {
				key = p.expr(precLowest)
			}
			p.expect(token.COLON, "after map key")
			m.Keys = append(m.Keys, key)
			m.Values = append(m.Values, p.expr(precLowest))
			if !p.match(token.COMMA) {
				break
			}
		}
		p.expect(token.RBRACE, "to close map")
		return m
	case token.OPERATION:
		return p.funcRest(t.Pos, "")
	}
	p.failAt(t, "expected an expression, found %s", describe(t))
	return nil
}

// interpolation turns "a{x}b{y}c" (STRING_PART STRING_PART STRING_END with
// expressions between them) into (("a" + x) + "b" + y) + "c". The chain
// always starts with a string, so + concatenates even when x and y are numbers.
func (p *Parser) interpolation(first token.Token) ast.Expr {
	var left ast.Expr = &ast.StringLit{P: first.Pos, Value: first.Lit}
	for {
		x := p.expr(precLowest)
		left = &ast.BinaryExpr{P: x.Pos(), Op: token.PLUS, L: left, R: x}
		t := p.cur()
		if t.Type != token.STRING_PART && t.Type != token.STRING_END {
			p.failAt(t, "expected '}' to close interpolation, found %s", describe(t))
		}
		p.advance()
		if t.Lit != "" {
			left = &ast.BinaryExpr{P: t.Pos, Op: token.PLUS, L: left, R: &ast.StringLit{P: t.Pos, Value: t.Lit}}
		}
		if t.Type == token.STRING_END {
			return left
		}
	}
}

func (p *Parser) infix(left ast.Expr, prec int) ast.Expr {
	t := p.advance()
	switch t.Type {
	case token.LPAREN:
		call := &ast.CallExpr{P: t.Pos, Fn: left}
		for !p.check(token.RPAREN) {
			call.Args = append(call.Args, p.expr(precLowest))
			if !p.match(token.COMMA) {
				break
			}
		}
		p.expect(token.RPAREN, "to close call arguments")
		if len(call.Args) > 255 {
			p.failAt(t, "cannot pass more than 255 arguments")
		}
		return call
	case token.LBRACKET:
		idx := p.expr(precLowest)
		p.expect(token.RBRACKET, "to close index")
		return &ast.IndexExpr{P: t.Pos, X: left, Index: idx}
	case token.DOT:
		p.nl()
		name := p.cur()
		if name.Type != token.IDENT && !name.Type.IsKeyword() {
			p.failAt(name, "expected field name after '.', found %s", describe(name))
		}
		p.advance()
		lit := name.Lit
		if name.Type.IsKeyword() {
			lit = name.Type.String()
		}
		return &ast.FieldExpr{P: t.Pos, X: left, Name: lit}
	case token.QUESTION:
		// The middle operand sits between '?' and ':', which bracket it like
		// parentheses, so any expression may go there. The last operand is
		// parsed at the ternary's own level minus one, which makes it
		// right-associative: a ? b : c ? d : e is a ? b : (c ? d : e).
		then := p.expr(precLowest)
		p.expect(token.COLON, "in 'cond ? a : b'")
		els := p.expr(prec - 1)
		return &ast.CondExpr{P: t.Pos, Cond: left, Then: then, Else: els}
	case token.STAR_STAR:
		// Right-associative: parse the right side at a lower minimum so
		// another '**' binds into it. 2 ** 3 ** 2 is 2 ** 9.
		right := p.expr(prec - 1)
		return &ast.BinaryExpr{P: t.Pos, Op: t.Type, L: left, R: right}
	default:
		right := p.expr(prec)
		return &ast.BinaryExpr{P: t.Pos, Op: t.Type, L: left, R: right}
	}
}
