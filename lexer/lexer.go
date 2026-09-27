// Package lexer turns Sern source text into tokens.
package lexer

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"sern/token"
)

// Error is a lexical error with a source position.
type Error struct {
	Pos token.Pos
	Msg string
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Pos, e.Msg) }

// Lexer scans a source string one token at a time.
type Lexer struct {
	src        string
	pos        int
	line, col  int
	sawNewline bool
	// interp has one frame per string whose {expr} is being lexed. It is a
	// stack because the expression can hold braces or another interpolated
	// string, and each needs its own depth to find its closing '}'.
	interp []interpFrame
	// newlineTokens switches from the NewlineBefore flag to emitting one
	// NEWLINE token per run of line breaks (see NewNewlineTokens).
	newlineTokens bool
	nlPos         token.Pos
}

type interpFrame struct {
	depth int       // unclosed '{' inside the expression
	start token.Pos // the string's opening quote, for errors
}

// New creates a lexer over src.
func New(src string) *Lexer { return &Lexer{src: src, line: 1, col: 1} }

// NewNewlineTokens creates a lexer that emits NEWLINE tokens instead of
// setting NewlineBefore. It is an alternative design;
// parser.ParseNewlineTokens consumes it.
func NewNewlineTokens(src string) *Lexer {
	l := New(src)
	l.newlineTokens = true
	return l
}

// Tokenize scans the whole input, returning every token up to and including EOF.
func Tokenize(src string) ([]token.Token, error) { return New(src).All() }

// All scans the rest of the input, returning every token up to and including EOF.
func (l *Lexer) All() ([]token.Token, error) {
	var toks []token.Token
	for {
		t, err := l.Next()
		if err != nil {
			return toks, err
		}
		toks = append(toks, t)
		if t.Type == token.EOF {
			return toks, nil
		}
	}
}

func (l *Lexer) peekByte(off int) byte {
	i := l.pos + off
	if i >= len(l.src) {
		return 0
	}
	return l.src[i]
}

func (l *Lexer) advance() byte {
	c := l.src[l.pos]
	l.pos++
	if c == '\n' {
		l.line++
		l.col = 1
	} else if !utf8.RuneStart(c) {
		// A UTF-8 continuation byte: columns count characters, not bytes.
	} else {
		l.col++
	}
	return c
}

func (l *Lexer) here() token.Pos { return token.Pos{Line: l.line, Col: l.col} }

func (l *Lexer) skipSpaceAndComments() error {
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch {
		case c == '\n':
			if !l.sawNewline {
				l.nlPos = l.here()
			}
			l.sawNewline = true
			l.advance()
		case c == ' ' || c == '\t' || c == '\r':
			l.advance()
		case c == '/' && l.peekByte(1) == '/':
			for l.pos < len(l.src) && l.src[l.pos] != '\n' {
				l.advance()
			}
		case c == '/' && l.peekByte(1) == '*':
			start := l.here()
			l.advance()
			l.advance()
			closed := false
			for l.pos < len(l.src) {
				if l.src[l.pos] == '*' && l.peekByte(1) == '/' {
					l.advance()
					l.advance()
					closed = true
					break
				}
				if l.src[l.pos] == '\n' {
					if !l.sawNewline {
						l.nlPos = l.here()
					}
					l.sawNewline = true
				}
				l.advance()
			}
			if !closed {
				return &Error{start, "unterminated block comment"}
			}
		default:
			return nil
		}
	}
	return nil
}

// Next returns the next token.
func (l *Lexer) Next() (token.Token, error) {
	if err := l.skipSpaceAndComments(); err != nil {
		return token.Token{}, err
	}
	if n := len(l.interp); n > 0 && (l.sawNewline || l.pos >= len(l.src)) {
		return token.Token{}, &Error{l.interp[n-1].start, "unterminated string (strings cannot span lines)"}
	}
	nl := l.sawNewline
	l.sawNewline = false
	if l.newlineTokens && nl {
		return token.Token{Type: token.NEWLINE, Lit: "\\n", Pos: l.nlPos}, nil
	}
	pos := l.here()
	mk := func(t token.Type, lit string) token.Token {
		return token.Token{Type: t, Lit: lit, Pos: pos, NewlineBefore: nl}
	}
	if l.pos >= len(l.src) {
		return mk(token.EOF, ""), nil
	}

	c := l.src[l.pos]
	switch {
	case l.identStart():
		start := l.pos
		for l.pos < len(l.src) {
			r, size := utf8.DecodeRuneInString(l.src[l.pos:])
			if !isLetterRune(r) && !unicode.IsDigit(r) {
				break
			}
			for range size {
				l.advance()
			}
		}
		word := l.src[start:l.pos]
		return mk(token.LookupIdent(word), word), nil
	case isDigit(c):
		t, lit, err := l.number(pos)
		if err != nil {
			return token.Token{}, err
		}
		return mk(t, lit), nil
	case c == '"':
		l.advance()
		s, interp, err := l.str(pos)
		if err != nil {
			return token.Token{}, err
		}
		if interp {
			l.interp = append(l.interp, interpFrame{start: pos})
			return mk(token.STRING_PART, s), nil
		}
		return mk(token.STRING, s), nil
	}

	l.advance()
	two := func(next byte, yes, no token.Type) token.Token {
		if l.pos < len(l.src) && l.src[l.pos] == next {
			l.advance()
			return mk(yes, yes.String())
		}
		return mk(no, no.String())
	}
	switch c {
	case '+':
		return two('=', token.PLUS_ASSIGN, token.PLUS), nil
	case '-':
		return two('=', token.MINUS_ASSIGN, token.MINUS), nil
	case '*':
		if l.peekByte(0) == '*' {
			l.advance()
			return mk(token.STAR_STAR, "**"), nil
		}
		return two('=', token.STAR_ASSIGN, token.STAR), nil
	case '/':
		return two('=', token.SLASH_ASSIGN, token.SLASH), nil
	case '%':
		return two('=', token.PERCENT_ASSIGN, token.PERCENT), nil
	case '=':
		if l.peekByte(0) == '>' {
			l.advance()
			return mk(token.FAT_ARROW, "=>"), nil
		}
		return two('=', token.EQ, token.ASSIGN), nil
	case '!':
		return two('=', token.NEQ, token.BANG), nil
	case '<':
		return two('=', token.LE, token.LT), nil
	case '>':
		return two('=', token.GE, token.GT), nil
	case '&':
		if l.peekByte(0) == '&' {
			l.advance()
			return mk(token.AND, "&&"), nil
		}
		return token.Token{}, &Error{pos, "unexpected '&' (did you mean '&&'?)"}
	case '|':
		if l.peekByte(0) == '|' {
			l.advance()
			return mk(token.OR, "||"), nil
		}
		return token.Token{}, &Error{pos, "unexpected '|' (did you mean '||'?)"}
	case ',':
		return mk(token.COMMA, ","), nil
	case ';':
		return mk(token.SEMICOLON, ";"), nil
	case ':':
		return mk(token.COLON, ":"), nil
	case '?':
		return mk(token.QUESTION, "?"), nil
	case '.':
		return mk(token.DOT, "."), nil
	case '(':
		return mk(token.LPAREN, "("), nil
	case ')':
		return mk(token.RPAREN, ")"), nil
	case '{':
		if n := len(l.interp); n > 0 {
			l.interp[n-1].depth++
		}
		return mk(token.LBRACE, "{"), nil
	case '}':
		if n := len(l.interp); n > 0 {
			f := &l.interp[n-1]
			if f.depth > 0 {
				f.depth--
				return mk(token.RBRACE, "}"), nil
			}
			// This '}' closes an interpolation: resume the string.
			s, interp, err := l.str(f.start)
			if err != nil {
				return token.Token{}, err
			}
			if interp {
				return mk(token.STRING_PART, s), nil
			}
			l.interp = l.interp[:n-1]
			return mk(token.STRING_END, s), nil
		}
		return mk(token.RBRACE, "}"), nil
	case '[':
		return mk(token.LBRACKET, "["), nil
	case ']':
		return mk(token.RBRACKET, "]"), nil
	}
	if c >= utf8.RuneSelf {
		l.pos--
		r, _ := utf8.DecodeRuneInString(l.src[l.pos:])
		return token.Token{}, &Error{pos, fmt.Sprintf("unexpected character %q", r)}
	}
	return token.Token{}, &Error{pos, fmt.Sprintf("unexpected character %q", c)}
}

func (l *Lexer) number(pos token.Pos) (token.Type, string, error) {
	if l.peekByte(0) == '0' {
		switch l.peekByte(1) {
		case 'x', 'X':
			return l.radix("0x", "hex", isHexDigit)
		case 'b', 'B':
			return l.radix("0b", "binary", func(c byte) bool { return c == '0' || c == '1' })
		}
	}
	var b strings.Builder
	digits := func() {
		for l.pos < len(l.src) && (isDigit(l.src[l.pos]) || l.src[l.pos] == '_') {
			if c := l.advance(); c != '_' {
				b.WriteByte(c)
			}
		}
	}
	digits()
	typ := token.INT
	if l.peekByte(0) == '.' && isDigit(l.peekByte(1)) {
		typ = token.FLOAT
		b.WriteByte(l.advance())
		digits()
	}
	if c := l.peekByte(0); c == 'e' || c == 'E' {
		n := l.peekByte(1)
		if isDigit(n) || ((n == '+' || n == '-') && isDigit(l.peekByte(2))) {
			typ = token.FLOAT
			b.WriteByte(l.advance())
			if n == '+' || n == '-' {
				b.WriteByte(l.advance())
			}
			digits()
		}
	}
	if l.identStart() {
		r, _ := utf8.DecodeRuneInString(l.src[l.pos:])
		return 0, "", &Error{l.here(), fmt.Sprintf("invalid character %q in number", r)}
	}
	return typ, b.String(), nil
}

// radix scans a prefixed integer such as 0xFF or 0b1010. The literal keeps
// its (lower-cased) prefix so the parser can pick the base.
func (l *Lexer) radix(prefix, name string, ok func(byte) bool) (token.Type, string, error) {
	prefixPos := l.here()
	l.advance()
	l.advance()
	var b strings.Builder
	b.WriteString(prefix)
	for l.pos < len(l.src) && (ok(l.src[l.pos]) || l.src[l.pos] == '_') {
		if c := l.advance(); c != '_' {
			b.WriteByte(c)
		}
	}
	if b.Len() == len(prefix) {
		return 0, "", &Error{prefixPos, fmt.Sprintf("expected %s digits after %s", name, prefix)}
	}
	if l.identStart() || isDigit(l.peekByte(0)) {
		r, _ := utf8.DecodeRuneInString(l.src[l.pos:])
		return 0, "", &Error{l.here(), fmt.Sprintf("invalid character %q in number", r)}
	}
	return token.INT, b.String(), nil
}

// str scans string text after an opening quote (or after the '}' that
// closes an interpolation). It stops at the closing quote, or at a '{' that
// starts an interpolation, in which case interp is true. "{}" is literal so
// fmt templates keep working. pos is the opening quote, for errors.
func (l *Lexer) str(pos token.Pos) (s string, interp bool, err error) {
	var b strings.Builder
	for {
		if l.pos >= len(l.src) || l.src[l.pos] == '\n' {
			return "", false, &Error{pos, "unterminated string (strings cannot span lines)"}
		}
		c := l.advance()
		if c == '"' {
			return b.String(), false, nil
		}
		if c == '{' {
			if l.peekByte(0) != '}' {
				return b.String(), true, nil
			}
			b.WriteByte(c)
			b.WriteByte(l.advance())
			continue
		}
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		if l.pos >= len(l.src) {
			return "", false, &Error{pos, "unterminated string"}
		}
		escPos := l.here()
		switch e := l.advance(); e {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case '0':
			b.WriteByte(0)
		case '"':
			b.WriteByte('"')
		case '\\':
			b.WriteByte('\\')
		case '{':
			b.WriteByte('{')
		default:
			return "", false, &Error{escPos, fmt.Sprintf("unknown escape sequence \\%c", e)}
		}
	}
}

// identStart reports whether an identifier starts at the current position.
func (l *Lexer) identStart() bool {
	if l.pos >= len(l.src) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(l.src[l.pos:])
	return isLetterRune(r)
}

func isLetterRune(r rune) bool { return r == '_' || unicode.IsLetter(r) }
func isDigit(c byte) bool      { return c >= '0' && c <= '9' }
func isHexDigit(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}
