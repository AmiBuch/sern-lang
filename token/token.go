// Package token defines the lexical tokens of the Sern language.
//
// Sern is the language SERN's machines speak. Its keywords borrow from the
// Future Gadget Lab: functions are "operations", variables are "lab
// members", loops "time leap", and every operation ends with "kongroo".
package token

import "fmt"

// Type identifies the kind of a token.
type Type int

const (
	ILLEGAL Type = iota
	EOF
	NEWLINE // only in the lexer's NewlineTokens mode

	IDENT
	INT
	FLOAT
	STRING
	STRING_PART // text before an interpolated {expr}
	STRING_END  // text after the last interpolated {expr}

	ASSIGN
	PLUS_ASSIGN
	MINUS_ASSIGN
	STAR_ASSIGN
	SLASH_ASSIGN
	PERCENT_ASSIGN

	PLUS
	MINUS
	STAR
	STAR_STAR
	SLASH
	PERCENT
	BANG

	EQ
	NEQ
	LT
	LE
	GT
	GE
	AND
	OR

	COMMA
	SEMICOLON
	COLON
	QUESTION
	FAT_ARROW // => in match arms
	DOT
	LPAREN
	RPAREN
	LBRACE
	RBRACE
	LBRACKET
	RBRACKET

	keywordsBegin
	OPERATION // function declaration / literal
	LABMEM    // variable declaration
	KONGROO   // return
	IF
	ELSE
	TIMELEAP // while loop
	READING  // for-each loop (Reading Steiner)
	IN
	BREAK
	CONTINUE
	TRUE
	FALSE
	NIL
	CONST // compile-time constant
	MATCH
	keywordsEnd
)

var names = map[Type]string{
	ILLEGAL: "ILLEGAL",
	EOF:     "EOF",
	NEWLINE: "NEWLINE",
	IDENT:   "IDENT",
	INT:     "INT",
	FLOAT:   "FLOAT",
	STRING:  "STRING",

	STRING_PART: "STRING_PART",
	STRING_END:  "STRING_END",

	ASSIGN:         "=",
	PLUS_ASSIGN:    "+=",
	MINUS_ASSIGN:   "-=",
	STAR_ASSIGN:    "*=",
	SLASH_ASSIGN:   "/=",
	PERCENT_ASSIGN: "%=",

	PLUS:      "+",
	MINUS:     "-",
	STAR:      "*",
	STAR_STAR: "**",
	SLASH:     "/",
	PERCENT:   "%",
	BANG:      "!",

	EQ:  "==",
	NEQ: "!=",
	LT:  "<",
	LE:  "<=",
	GT:  ">",
	GE:  ">=",
	AND: "&&",
	OR:  "||",

	COMMA:     ",",
	SEMICOLON: ";",
	COLON:     ":",
	QUESTION:  "?",
	FAT_ARROW: "=>",
	DOT:       ".",
	LPAREN:    "(",
	RPAREN:    ")",
	LBRACE:    "{",
	RBRACE:    "}",
	LBRACKET:  "[",
	RBRACKET:  "]",

	OPERATION: "operation",
	LABMEM:    "labmem",
	KONGROO:   "kongroo",
	IF:        "if",
	ELSE:      "else",
	TIMELEAP:  "timeleap",
	READING:   "reading",
	IN:        "in",
	BREAK:     "break",
	CONTINUE:  "continue",
	TRUE:      "true",
	FALSE:     "false",
	NIL:       "nil",
	CONST:     "const",
	MATCH:     "match",
}

func (t Type) String() string {
	if s, ok := names[t]; ok {
		return s
	}
	return fmt.Sprintf("Type(%d)", int(t))
}

// IsKeyword reports whether t is a reserved word.
func (t Type) IsKeyword() bool { return t > keywordsBegin && t < keywordsEnd }

var keywords = func() map[string]Type {
	m := map[string]Type{}
	for t := keywordsBegin + 1; t < keywordsEnd; t++ {
		m[names[t]] = t
	}
	return m
}()

// LookupIdent returns the keyword type for word, or IDENT.
func LookupIdent(word string) Type {
	if t, ok := keywords[word]; ok {
		return t
	}
	return IDENT
}

// Keywords returns every reserved word (used by docs and the REPL).
func Keywords() []string {
	var out []string
	for t := keywordsBegin + 1; t < keywordsEnd; t++ {
		out = append(out, names[t])
	}
	return out
}

// Pos is a 1-based source position.
type Pos struct {
	Line int
	Col  int
}

func (p Pos) String() string { return fmt.Sprintf("%d:%d", p.Line, p.Col) }

// Token is a single lexical unit.
type Token struct {
	Type Type
	Lit  string
	Pos  Pos
	// NewlineBefore is true when at least one newline separates this token
	// from the previous one. The parser uses it to make semicolons optional.
	NewlineBefore bool
}

func (t Token) String() string {
	switch t.Type {
	case IDENT, INT, FLOAT:
		return fmt.Sprintf("%-10s %s", t.Type, t.Lit)
	case STRING, STRING_PART, STRING_END:
		return fmt.Sprintf("%-10s %q", t.Type, t.Lit)
	default:
		return t.Type.String()
	}
}
