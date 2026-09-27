// Package ast defines the abstract syntax tree produced by the Sern parser.
//
// The same tree feeds every backend: the bytecode compiler today and the
// LLVM code generator in phase 2.
package ast

import (
	"fmt"
	"strconv"
	"strings"

	"sern/token"
)

// Node is any syntax tree node.
type Node interface{ Pos() token.Pos }

// Stmt is a statement node.
type Stmt interface {
	Node
	stmtNode()
}

// Expr is an expression node.
type Expr interface {
	Node
	exprNode()
}

// Program is a whole Sern source file.
type Program struct {
	Stmts []Stmt
}

// ---- statements ----

type (
	// LabmemStmt declares a variable: labmem name = value
	LabmemStmt struct {
		P     token.Pos
		Name  string
		Value Expr
	}
	// OperationStmt declares a named function: operation name(params) { ... }
	OperationStmt struct {
		P  token.Pos
		Fn *FuncLit
	}
	// KongrooStmt returns from an operation: kongroo [value]
	KongrooStmt struct {
		P     token.Pos
		Value Expr // may be nil
	}
	// IfStmt is if cond { } else { }
	IfStmt struct {
		P    token.Pos
		Cond Expr
		Then *BlockStmt
		Else Stmt // *BlockStmt, *IfStmt, or nil
	}
	// TimeleapStmt loops while its condition holds.
	TimeleapStmt struct {
		P    token.Pos
		Cond Expr
		Body *BlockStmt
	}
	// ReadingStmt iterates: reading x in xs { } / reading i, x in xs { }
	ReadingStmt struct {
		P      token.Pos
		First  string
		Second string // empty when only one variable is bound
		Iter   Expr
		Body   *BlockStmt
	}
	BreakStmt    struct{ P token.Pos }
	ContinueStmt struct{ P token.Pos }
	BlockStmt    struct {
		P     token.Pos
		Stmts []Stmt
	}
	ExprStmt struct {
		P token.Pos
		X Expr
	}
	// AssignStmt is target op value, where op is =, +=, -=, *=, /=, %=.
	AssignStmt struct {
		P      token.Pos
		Target Expr // *Ident, *IndexExpr or *FieldExpr
		Op     token.Type
		Value  Expr
	}
)

// ---- expressions ----

type (
	Ident struct {
		P    token.Pos
		Name string
	}
	IntLit struct {
		P     token.Pos
		Value int64
	}
	FloatLit struct {
		P     token.Pos
		Value float64
	}
	StringLit struct {
		P     token.Pos
		Value string
	}
	BoolLit struct {
		P     token.Pos
		Value bool
	}
	NilLit  struct{ P token.Pos }
	ListLit struct {
		P     token.Pos
		Elems []Expr
	}
	MapLit struct {
		P      token.Pos
		Keys   []Expr
		Values []Expr
	}
	UnaryExpr struct {
		P  token.Pos
		Op token.Type
		X  Expr
	}
	// BinaryExpr covers arithmetic, comparison and the short-circuit && / ||.
	BinaryExpr struct {
		P    token.Pos
		Op   token.Type
		L, R Expr
	}
	CallExpr struct {
		P    token.Pos
		Fn   Expr
		Args []Expr
	}
	IndexExpr struct {
		P     token.Pos
		X     Expr
		Index Expr
	}
	FieldExpr struct {
		P    token.Pos
		X    Expr
		Name string
	}
	// FuncLit is an operation, named or anonymous.
	FuncLit struct {
		P      token.Pos
		Name   string
		Params []string
		Body   *BlockStmt
	}
	// ConstStmt declares a compile-time constant: const NAME = expr.
	ConstStmt struct {
		P     token.Pos
		Name  string
		Value Expr
	}
	// MatchStmt is match Subject { pattern => body, ..., _ => body }.
	MatchStmt struct {
		P       token.Pos
		Subject Expr
		Arms    []MatchArm
	}
	// CondExpr is the ternary cond ? Then : Else.
	CondExpr struct {
		P                token.Pos
		Cond, Then, Else Expr
	}
)

func (n *LabmemStmt) Pos() token.Pos    { return n.P }
func (n *OperationStmt) Pos() token.Pos { return n.P }
func (n *KongrooStmt) Pos() token.Pos   { return n.P }
func (n *IfStmt) Pos() token.Pos        { return n.P }
func (n *TimeleapStmt) Pos() token.Pos  { return n.P }
func (n *ReadingStmt) Pos() token.Pos   { return n.P }
func (n *BreakStmt) Pos() token.Pos     { return n.P }
func (n *ContinueStmt) Pos() token.Pos  { return n.P }
func (n *BlockStmt) Pos() token.Pos     { return n.P }
func (n *ExprStmt) Pos() token.Pos      { return n.P }
func (n *AssignStmt) Pos() token.Pos    { return n.P }

func (*LabmemStmt) stmtNode()    {}
func (*OperationStmt) stmtNode() {}
func (*KongrooStmt) stmtNode()   {}
func (*IfStmt) stmtNode()        {}
func (*TimeleapStmt) stmtNode()  {}
func (*ReadingStmt) stmtNode()   {}
func (*BreakStmt) stmtNode()     {}
func (*ContinueStmt) stmtNode()  {}
func (*BlockStmt) stmtNode()     {}
func (*ExprStmt) stmtNode()      {}
func (*AssignStmt) stmtNode()    {}

func (n *Ident) Pos() token.Pos      { return n.P }
func (n *IntLit) Pos() token.Pos     { return n.P }
func (n *FloatLit) Pos() token.Pos   { return n.P }
func (n *StringLit) Pos() token.Pos  { return n.P }
func (n *BoolLit) Pos() token.Pos    { return n.P }
func (n *NilLit) Pos() token.Pos     { return n.P }
func (n *ListLit) Pos() token.Pos    { return n.P }
func (n *MapLit) Pos() token.Pos     { return n.P }
func (n *UnaryExpr) Pos() token.Pos  { return n.P }
func (n *BinaryExpr) Pos() token.Pos { return n.P }
func (n *CallExpr) Pos() token.Pos   { return n.P }
func (n *IndexExpr) Pos() token.Pos  { return n.P }
func (n *FieldExpr) Pos() token.Pos  { return n.P }
func (n *FuncLit) Pos() token.Pos    { return n.P }
func (n *CondExpr) Pos() token.Pos   { return n.P }
func (n *ConstStmt) Pos() token.Pos  { return n.P }
func (n *MatchStmt) Pos() token.Pos  { return n.P }

func (*ConstStmt) stmtNode() {}
func (*MatchStmt) stmtNode() {}

// MatchArm is one "pattern => body" of a match. Pattern is nil for the
// wildcard _.
type MatchArm struct {
	P       token.Pos
	Pattern Expr
	Body    Stmt
}

func (*Ident) exprNode()      {}
func (*IntLit) exprNode()     {}
func (*FloatLit) exprNode()   {}
func (*StringLit) exprNode()  {}
func (*BoolLit) exprNode()    {}
func (*NilLit) exprNode()     {}
func (*ListLit) exprNode()    {}
func (*MapLit) exprNode()     {}
func (*UnaryExpr) exprNode()  {}
func (*BinaryExpr) exprNode() {}
func (*CallExpr) exprNode()   {}
func (*IndexExpr) exprNode()  {}
func (*FieldExpr) exprNode()  {}
func (*FuncLit) exprNode()    {}
func (*CondExpr) exprNode()   {}

// ---- printing ----

// ExprString renders an expression compactly with explicit parentheses,
// which makes operator precedence visible in `ibn-5100 ast` output.
func ExprString(e Expr) string {
	switch n := e.(type) {
	case *Ident:
		return n.Name
	case *IntLit:
		return strconv.FormatInt(n.Value, 10)
	case *FloatLit:
		return strconv.FormatFloat(n.Value, 'g', -1, 64)
	case *StringLit:
		return strconv.Quote(n.Value)
	case *BoolLit:
		return strconv.FormatBool(n.Value)
	case *NilLit:
		return "nil"
	case *ListLit:
		return "[" + joinExprs(n.Elems) + "]"
	case *MapLit:
		parts := make([]string, len(n.Keys))
		for i := range n.Keys {
			parts[i] = ExprString(n.Keys[i]) + ": " + ExprString(n.Values[i])
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case *UnaryExpr:
		return "(" + n.Op.String() + ExprString(n.X) + ")"
	case *BinaryExpr:
		return "(" + ExprString(n.L) + " " + n.Op.String() + " " + ExprString(n.R) + ")"
	case *CallExpr:
		return ExprString(n.Fn) + "(" + joinExprs(n.Args) + ")"
	case *IndexExpr:
		return ExprString(n.X) + "[" + ExprString(n.Index) + "]"
	case *FieldExpr:
		return ExprString(n.X) + "." + n.Name
	case *FuncLit:
		return "operation(" + strings.Join(n.Params, ", ") + ") {...}"
	case *CondExpr:
		return "(" + ExprString(n.Cond) + " ? " + ExprString(n.Then) + " : " + ExprString(n.Else) + ")"
	}
	return fmt.Sprintf("<?%T>", e)
}

func joinExprs(es []Expr) string {
	parts := make([]string, len(es))
	for i, e := range es {
		parts[i] = ExprString(e)
	}
	return strings.Join(parts, ", ")
}

// Dump renders a program as an indented tree.
func Dump(p *Program) string {
	var b strings.Builder
	b.WriteString("Program\n")
	for _, s := range p.Stmts {
		dumpStmt(&b, s, 1)
	}
	return b.String()
}

func dumpStmt(b *strings.Builder, s Stmt, depth int) {
	ind := strings.Repeat("  ", depth)
	line := func(format string, args ...any) {
		fmt.Fprintf(b, "%s%s  @%s\n", ind, fmt.Sprintf(format, args...), s.Pos())
	}
	switch n := s.(type) {
	case *LabmemStmt:
		line("Labmem %s = %s", n.Name, ExprString(n.Value))
	case *OperationStmt:
		line("Operation %s(%s)", n.Fn.Name, strings.Join(n.Fn.Params, ", "))
		for _, st := range n.Fn.Body.Stmts {
			dumpStmt(b, st, depth+1)
		}
	case *KongrooStmt:
		if n.Value == nil {
			line("Kongroo")
		} else {
			line("Kongroo %s", ExprString(n.Value))
		}
	case *IfStmt:
		line("If %s", ExprString(n.Cond))
		for _, st := range n.Then.Stmts {
			dumpStmt(b, st, depth+1)
		}
		if n.Else != nil {
			fmt.Fprintf(b, "%sElse\n", ind)
			if blk, ok := n.Else.(*BlockStmt); ok {
				for _, st := range blk.Stmts {
					dumpStmt(b, st, depth+1)
				}
			} else {
				dumpStmt(b, n.Else, depth+1)
			}
		}
	case *TimeleapStmt:
		line("Timeleap %s", ExprString(n.Cond))
		for _, st := range n.Body.Stmts {
			dumpStmt(b, st, depth+1)
		}
	case *ReadingStmt:
		vars := n.First
		if n.Second != "" {
			vars += ", " + n.Second
		}
		line("Reading %s in %s", vars, ExprString(n.Iter))
		for _, st := range n.Body.Stmts {
			dumpStmt(b, st, depth+1)
		}
	case *BreakStmt:
		line("Break")
	case *ContinueStmt:
		line("Continue")
	case *BlockStmt:
		line("Block")
		for _, st := range n.Stmts {
			dumpStmt(b, st, depth+1)
		}
	case *ExprStmt:
		line("Expr %s", ExprString(n.X))
	case *AssignStmt:
		line("Assign %s %s %s", ExprString(n.Target), n.Op, ExprString(n.Value))
	case *ConstStmt:
		line("Const %s = %s", n.Name, ExprString(n.Value))
	case *MatchStmt:
		line("Match %s", ExprString(n.Subject))
		for _, arm := range n.Arms {
			pat := "_"
			if arm.Pattern != nil {
				pat = ExprString(arm.Pattern)
			}
			fmt.Fprintf(b, "%s  %s =>\n", ind, pat)
			dumpStmt(b, arm.Body, depth+2)
		}
	default:
		line("<?%T>", s)
	}
}
