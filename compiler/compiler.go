// Package compiler translates a Sern syntax tree into bytecode.
//
// It is a single-pass compiler in the style of clox (Crafting Interpreters):
// it walks the AST once, emitting instructions into the chunk of the
// operation currently being compiled. Local variables are resolved to stack
// slots at compile time, globals to slots in a shared symbol table, and
// variables captured by inner operations become upvalues.
package compiler

import (
	"cmp"
	"fmt"
	"slices"

	"sern/ast"
	"sern/object"
	"sern/token"
	"sern/vm"
)

// Error is a compile-time (semantic) error.
type Error struct {
	Pos token.Pos
	Msg string
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Pos, e.Msg) }

// Options control compilation.
type Options struct {
	// Repl makes top-level expression statements print their value.
	Repl bool
	// NoAutoMain disables the implicit call to operation main().
	NoAutoMain bool
	// NoSuperinstructions disables ADD_CONST/SUB_CONST (for benchmarks).
	NoSuperinstructions bool
	// NoTailCalls compiles kongroo f(x) as CALL; RETURN (for comparison).
	NoTailCalls bool
	// Warn, if set, receives warnings such as unused local variables.
	Warn func(pos token.Pos, msg string)
}

const (
	maxLocals    = 1024
	maxConstants = 1 << 16
	maxJump      = 1<<16 - 1
)

type local struct {
	name     string
	depth    int
	captured bool
	read     bool      // for unused-variable warnings
	isFunc   bool      // declared by a local 'operation'
	pos      token.Pos // declaration, for warnings
}

type loopCtx struct {
	start  int // bytecode offset that 'continue' jumps back to
	depth  int // scope depth outside the loop body
	breaks []int
}

type constKey struct {
	k object.Kind
	i int64
	f float64
	s string
}

// funcState is the compiler state for one operation being compiled.
// funcStates form a stack via parent, mirroring lexical nesting.
type funcState struct {
	parent   *funcState
	proto    *object.FunctionProto
	locals   []local
	upvalues []object.UpvalueDesc
	depth    int
	loops    []*loopCtx
	isScript bool
	consts   map[constKey]uint16
}

type globalRef struct {
	name string
	pos  token.Pos
}

// Compiler compiles programs against a shared symbol table.
type Compiler struct {
	fs   *funcState
	syms *object.Symbols
	opts Options
	line int
	refs []globalRef
	// consts holds compile-time constants (const NAME = ...). It lives on
	// the Compiler, not per program, so REPL inputs see earlier consts.
	consts map[string]object.Value
	// warnings are buffered and reported in source order at the end of
	// Compile (scopes close innermost first, which would scramble them).
	warnings []warning
}

type warning struct {
	pos token.Pos
	msg string
}

type bailout struct{ err *Error }

// New creates a compiler. syms must already contain the builtins
// (marked Defined) so references to print, len, ... resolve.
func New(syms *object.Symbols, opts Options) *Compiler {
	return &Compiler{syms: syms, opts: opts, consts: map[string]object.Value{}}
}

// Compile compiles a whole program into the top-level "script" operation.
func (c *Compiler) Compile(prog *ast.Program) (proto *object.FunctionProto, err error) {
	defer func() {
		if r := recover(); r != nil {
			b, ok := r.(bailout)
			if !ok {
				panic(r)
			}
			proto, err = nil, b.err
		}
	}()
	c.refs = nil
	c.warnings = nil
	c.fs = &funcState{
		proto:    &object.FunctionProto{Name: "<script>", Chunk: &object.Chunk{}},
		isScript: true,
		consts:   map[constKey]uint16{},
	}
	// Slot 0 of every frame holds the callee itself.
	c.fs.locals = []local{{name: "", depth: 0}}

	// Constants are declared before anything is compiled, so an operation
	// can use a const declared further down the file.
	for _, s := range prog.Stmts {
		if n, ok := s.(*ast.ConstStmt); ok {
			c.declareConst(n)
		}
	}

	hasMain := false
	for _, s := range prog.Stmts {
		if op, ok := s.(*ast.OperationStmt); ok && op.Fn.Name == "main" {
			hasMain = true
		}
		c.stmt(s)
	}
	if hasMain && !c.opts.Repl && !c.opts.NoAutoMain {
		slot := c.syms.Slot("main")
		c.emitOp(object.OpGetGlobal)
		c.emitU16(slot)
		c.emitOp(object.OpCall)
		c.emitByte(0)
		c.emitOp(object.OpPop)
	}
	c.emitOp(object.OpNil)
	c.emitOp(object.OpReturn)

	// Every global that was read or assigned must be defined somewhere in
	// the program (or be a builtin). This catches typos at compile time.
	for _, r := range c.refs {
		if !c.syms.Defined[c.syms.Slot(r.name)] {
			c.errorf(r.pos, "'%s' is not defined on this worldline (no labmem or operation declares it)", r.name)
		}
	}
	slices.SortStableFunc(c.warnings, func(a, b warning) int {
		return cmp.Or(cmp.Compare(a.pos.Line, b.pos.Line), cmp.Compare(a.pos.Col, b.pos.Col))
	})
	for _, w := range c.warnings {
		c.opts.Warn(w.pos, w.msg)
	}
	return c.fs.proto, nil
}

func (c *Compiler) errorf(pos token.Pos, format string, args ...any) {
	panic(bailout{&Error{Pos: pos, Msg: fmt.Sprintf(format, args...)}})
}

// ---- emission helpers ----

func (c *Compiler) chunk() *object.Chunk { return c.fs.proto.Chunk }

func (c *Compiler) emitByte(b byte) {
	ch := c.chunk()
	ch.Code = append(ch.Code, b)
	ch.Lines = append(ch.Lines, c.line)
}

func (c *Compiler) emitOp(op object.Op) { c.emitByte(byte(op)) }

func (c *Compiler) emitU16(v int) {
	c.emitByte(byte(v >> 8))
	c.emitByte(byte(v))
}

func (c *Compiler) emitOpU16(op object.Op, v int) {
	c.emitOp(op)
	c.emitU16(v)
}

// emitJump emits a forward jump with a placeholder offset and returns the
// position of the offset so it can be patched once the target is known.
func (c *Compiler) emitJump(op object.Op) int {
	c.emitOp(op)
	c.emitU16(0xffff)
	return len(c.chunk().Code) - 2
}

func (c *Compiler) patchJump(at int) {
	code := c.chunk().Code
	off := len(code) - (at + 2)
	if off > maxJump {
		c.errorf(token.Pos{Line: c.line}, "jump too large (block exceeds 64KB of bytecode)")
	}
	code[at] = byte(off >> 8)
	code[at+1] = byte(off)
}

// emitLoop emits a backward jump to start.
func (c *Compiler) emitLoop(start int) {
	c.emitOp(object.OpLoop)
	off := len(c.chunk().Code) + 2 - start
	if off > maxJump {
		c.errorf(token.Pos{Line: c.line}, "loop body too large")
	}
	c.emitU16(off)
}

func (c *Compiler) constant(v object.Value) int {
	var key *constKey
	switch v.K {
	case object.KInt, object.KFloat, object.KString, object.KBool, object.KNil:
		k := constKey{k: v.K, i: v.I, f: v.F}
		if v.K == object.KString {
			k.s = v.AsString()
		}
		if idx, ok := c.fs.consts[k]; ok {
			return int(idx)
		}
		key = &k
	}
	ch := c.chunk()
	if len(ch.Consts) >= maxConstants {
		c.errorf(token.Pos{Line: c.line}, "too many constants in one operation")
	}
	ch.Consts = append(ch.Consts, v)
	idx := len(ch.Consts) - 1
	if key != nil {
		c.fs.consts[*key] = uint16(idx)
	}
	return idx
}

// ---- scopes and variables ----

func (c *Compiler) beginScope() { c.fs.depth++ }

func (c *Compiler) endScope() {
	fs := c.fs
	fs.depth--
	for len(fs.locals) > 0 && fs.locals[len(fs.locals)-1].depth > fs.depth {
		c.warnUnused(fs.locals[len(fs.locals)-1])
		if fs.locals[len(fs.locals)-1].captured {
			c.emitOp(object.OpCloseUpvalue)
		} else {
			c.emitOp(object.OpPop)
		}
		fs.locals = fs.locals[:len(fs.locals)-1]
	}
}

// warnUnused reports a local that was declared but never read. Names
// starting with '_' opt out, like Go's blank identifier; hidden compiler
// locals such as "(iter)" are skipped.
func (c *Compiler) warnUnused(l local) {
	if c.opts.Warn == nil || l.read || l.name == "" || l.name[0] == '_' || l.name[0] == '(' {
		return
	}
	msg := fmt.Sprintf("lab member '%s' is declared but never read", l.name)
	if l.isFunc {
		msg = fmt.Sprintf("operation '%s' is declared but never used", l.name)
	}
	c.warnings = append(c.warnings, warning{l.pos, msg})
}

// discardLocalsAbove emits pops for locals deeper than depth without
// forgetting them in the compiler (used by break/continue, which jump out
// of scopes that the straight-line code still closes normally).
func (c *Compiler) discardLocalsAbove(depth int) {
	fs := c.fs
	for i := len(fs.locals) - 1; i >= 0 && fs.locals[i].depth > depth; i-- {
		if fs.locals[i].captured {
			c.emitOp(object.OpCloseUpvalue)
		} else {
			c.emitOp(object.OpPop)
		}
	}
}

// isGlobalScope reports whether a declaration here creates a global.
func (c *Compiler) isGlobalScope() bool { return c.fs.isScript && c.fs.depth == 0 }

func (c *Compiler) addLocal(pos token.Pos, name string) {
	fs := c.fs
	for i := len(fs.locals) - 1; i >= 0 && fs.locals[i].depth == fs.depth; i-- {
		if fs.locals[i].name == name {
			c.errorf(pos, "lab member '%s' is already declared in this scope", name)
		}
	}
	if len(fs.locals) >= maxLocals {
		c.errorf(pos, "too many local variables in one operation (max %d)", maxLocals)
	}
	fs.locals = append(fs.locals, local{name: name, depth: fs.depth, pos: pos})
}

func resolveLocal(fs *funcState, name string) int {
	for i := len(fs.locals) - 1; i >= 0; i-- {
		if fs.locals[i].name == name {
			return i
		}
	}
	return -1
}

func addUpvalue(fs *funcState, index uint16, isLocal bool) int {
	for i, u := range fs.upvalues {
		if u.Index == index && u.IsLocal == isLocal {
			return i
		}
	}
	fs.upvalues = append(fs.upvalues, object.UpvalueDesc{IsLocal: isLocal, Index: index})
	return len(fs.upvalues) - 1
}

// resolveUpvalue finds name in an enclosing operation. If it is a local of
// the immediately enclosing operation it is captured directly; otherwise
// it is threaded through each intermediate operation's upvalue list.
func resolveUpvalue(fs *funcState, name string) int {
	if fs.parent == nil {
		return -1
	}
	if l := resolveLocal(fs.parent, name); l >= 0 {
		// Capturing counts as a read: the closure is where it's used.
		fs.parent.locals[l].captured = true
		fs.parent.locals[l].read = true
		return addUpvalue(fs, uint16(l), true)
	}
	if u := resolveUpvalue(fs.parent, name); u >= 0 {
		return addUpvalue(fs, uint16(u), false)
	}
	return -1
}

func (c *Compiler) loadName(pos token.Pos, name string) {
	if slot := resolveLocal(c.fs, name); slot >= 0 {
		c.fs.locals[slot].read = true
		c.emitOpU16(object.OpGetLocal, slot)
	} else if up := resolveUpvalue(c.fs, name); up >= 0 {
		c.emitOpU16(object.OpGetUpvalue, up)
	} else if v, ok := c.consts[name]; ok {
		c.emitValue(v) // a const compiles to its value, not GET_GLOBAL
	} else {
		c.refs = append(c.refs, globalRef{name, pos})
		c.emitOpU16(object.OpGetGlobal, c.syms.Slot(name))
	}
}

func (c *Compiler) storeName(pos token.Pos, name string) {
	if slot := resolveLocal(c.fs, name); slot >= 0 {
		c.emitOpU16(object.OpSetLocal, slot)
	} else if up := resolveUpvalue(c.fs, name); up >= 0 {
		c.emitOpU16(object.OpSetUpvalue, up)
	} else if _, ok := c.consts[name]; ok {
		c.errorf(pos, "cannot assign to '%s': it is a const", name)
	} else {
		c.refs = append(c.refs, globalRef{name, pos})
		c.emitOpU16(object.OpSetGlobal, c.syms.Slot(name))
	}
}

// ---- statements ----

func (c *Compiler) stmt(s ast.Stmt) {
	c.line = s.Pos().Line
	switch n := s.(type) {
	case *ast.LabmemStmt:
		c.expr(n.Value)
		c.line = n.P.Line
		if c.isGlobalScope() {
			c.checkNotConst(n.P, n.Name)
			c.emitOpU16(object.OpDefineGlobal, c.syms.Define(n.Name))
		} else {
			// The value is already on the stack, exactly where the new
			// local's slot is. Declaring it after compiling the value means
			// `labmem x = x` reads the outer x.
			c.addLocal(n.P, n.Name)
		}

	case *ast.OperationStmt:
		if c.isGlobalScope() {
			c.checkNotConst(n.P, n.Fn.Name)
			slot := c.syms.Define(n.Fn.Name)
			c.function(n.Fn)
			c.emitOpU16(object.OpDefineGlobal, slot)
		} else {
			// Declare first so the body can refer to itself (recursion).
			c.addLocal(n.P, n.Fn.Name)
			c.fs.locals[len(c.fs.locals)-1].isFunc = true
			c.function(n.Fn)
		}

	case *ast.KongrooStmt:
		if call, ok := n.Value.(*ast.CallExpr); ok && !c.opts.NoTailCalls {
			// kongroo f(x): nothing is left to do in this frame after the
			// call, so the callee can take the frame over (TAIL_CALL).
			c.expr(call.Fn)
			for _, a := range call.Args {
				c.expr(a)
			}
			c.line = call.P.Line
			c.emitOp(object.OpTailCall)
			c.emitByte(byte(len(call.Args)))
			return
		}
		if n.Value != nil {
			c.expr(n.Value)
		} else {
			c.emitOp(object.OpNil)
		}
		c.emitOp(object.OpReturn)

	case *ast.ExprStmt:
		c.expr(n.X)
		if c.opts.Repl && c.isGlobalScope() {
			c.emitOp(object.OpEcho)
		} else {
			c.emitOp(object.OpPop)
		}

	case *ast.AssignStmt:
		c.assign(n)

	case *ast.BlockStmt:
		c.beginScope()
		for _, st := range n.Stmts {
			c.stmt(st)
		}
		c.endScope()

	case *ast.IfStmt:
		c.expr(n.Cond)
		elseJump := c.emitJump(object.OpJumpIfFalse)
		c.stmt(n.Then)
		if n.Else != nil {
			endJump := c.emitJump(object.OpJump)
			c.patchJump(elseJump)
			c.stmt(n.Else)
			c.patchJump(endJump)
		} else {
			c.patchJump(elseJump)
		}

	case *ast.TimeleapStmt:
		start := len(c.chunk().Code)
		c.expr(n.Cond)
		exit := c.emitJump(object.OpJumpIfFalse)
		loop := &loopCtx{start: start, depth: c.fs.depth}
		c.fs.loops = append(c.fs.loops, loop)
		c.stmt(n.Body)
		c.fs.loops = c.fs.loops[:len(c.fs.loops)-1]
		c.emitLoop(start)
		c.patchJump(exit)
		for _, b := range loop.breaks {
			c.patchJump(b)
		}

	case *ast.ReadingStmt:
		c.reading(n)

	case *ast.ConstStmt:
		if !c.isGlobalScope() {
			c.errorf(n.P, "const is only allowed at the top level")
		}
		// Already declared by the pre-pass in Compile; emits no code.

	case *ast.MatchStmt:
		c.match(n)

	case *ast.BreakStmt:
		loop := c.currentLoop(n.P, "break")
		c.discardLocalsAbove(loop.depth)
		loop.breaks = append(loop.breaks, c.emitJump(object.OpJump))

	case *ast.ContinueStmt:
		loop := c.currentLoop(n.P, "continue")
		c.discardLocalsAbove(loop.depth)
		c.emitLoop(loop.start)

	default:
		c.errorf(s.Pos(), "internal: unknown statement %T", s)
	}
}

// declareConst evaluates a const's value at compile time and records it.
func (c *Compiler) declareConst(n *ast.ConstStmt) {
	if _, dup := c.consts[n.Name]; dup {
		c.errorf(n.P, "const '%s' is already declared", n.Name)
	}
	if i, ok := c.syms.Index[n.Name]; ok && c.syms.Defined[i] {
		c.errorf(n.P, "const '%s' would hide an existing global or builtin", n.Name)
	}
	c.line = n.P.Line
	v, ok := c.fold(n.Value)
	if !ok {
		c.errorf(n.Value.Pos(), "const '%s' must be a compile-time constant (literals, operators and earlier consts)", n.Name)
	}
	c.consts[n.Name] = v
}

func (c *Compiler) checkNotConst(pos token.Pos, name string) {
	if _, ok := c.consts[name]; ok {
		c.errorf(pos, "'%s' is a const and cannot be redeclared", name)
	}
}

// match compiles
//
//	match x { p1 => s1, p2 => s2, _ => s3 }
//
// into a chain of tests against the subject, which stays on the stack as a
// hidden local for the whole statement:
//
//	      <x>
//	      DUP; <p1>; EQ; JUMP_IF_FALSE next1
//	      <s1>; JUMP end
//	next1: DUP; <p2>; EQ; JUMP_IF_FALSE next2
//	      <s2>; JUMP end
//	next2: <s3>
//	end:  POP
func (c *Compiler) match(n *ast.MatchStmt) {
	c.beginScope()
	c.expr(n.Subject)
	c.addLocal(n.P, "(match)")
	var ends []int
	for i, arm := range n.Arms {
		c.line = arm.P.Line
		if arm.Pattern == nil {
			if i != len(n.Arms)-1 {
				c.errorf(n.Arms[i+1].P, "unreachable match arm: '_' already matches everything")
			}
			c.armBody(arm.Body)
			break
		}
		c.emitOp(object.OpDup)
		c.expr(arm.Pattern)
		c.line = arm.P.Line
		c.emitOp(object.OpEq)
		next := c.emitJump(object.OpJumpIfFalse)
		c.armBody(arm.Body)
		if i < len(n.Arms)-1 {
			ends = append(ends, c.emitJump(object.OpJump))
		}
		c.patchJump(next)
	}
	for _, e := range ends {
		c.patchJump(e)
	}
	c.endScope()
}

// armBody compiles a match arm in its own scope, so a local it declares
// is popped before the next arm DUPs the subject.
func (c *Compiler) armBody(s ast.Stmt) {
	c.beginScope()
	c.stmt(s)
	c.endScope()
}

func (c *Compiler) currentLoop(pos token.Pos, what string) *loopCtx {
	if len(c.fs.loops) == 0 {
		c.errorf(pos, "'%s' outside of a timeleap or reading loop", what)
	}
	return c.fs.loops[len(c.fs.loops)-1]
}

// reading compiles a for-each loop:
//
//	<iterable>  ITER_PREP          ; hidden local "(iter)"
//	start: FOR_ITER slot vars exit ; pushes 1 or 2 loop variables
//	       <body>
//	       pop loop variables (closing captured ones)
//	       LOOP start
//	exit:  POP                     ; the iterator
//
// Because the loop variables are popped (and closed) every iteration,
// closures created in the body capture a fresh binding each time.
func (c *Compiler) reading(n *ast.ReadingStmt) {
	c.beginScope()
	c.expr(n.Iter)
	c.line = n.P.Line
	c.emitOp(object.OpIterPrep)
	c.addLocal(n.P, "(iter)")
	iterSlot := len(c.fs.locals) - 1

	start := len(c.chunk().Code)
	vars := 1
	if n.Second != "" {
		vars = 2
	}
	c.emitOpU16(object.OpForIter, iterSlot)
	c.emitByte(byte(vars))
	c.emitU16(0xffff)
	exit := len(c.chunk().Code) - 2

	loop := &loopCtx{start: start, depth: c.fs.depth}
	c.fs.loops = append(c.fs.loops, loop)

	c.beginScope()
	c.addLocal(n.P, n.First)
	if n.Second != "" {
		c.addLocal(n.P, n.Second)
	}
	for _, st := range n.Body.Stmts {
		c.stmt(st)
	}
	c.line = n.P.Line
	c.endScope()

	c.fs.loops = c.fs.loops[:len(c.fs.loops)-1]
	c.emitLoop(start)
	c.patchJump(exit)
	for _, b := range loop.breaks {
		c.patchJump(b)
	}
	c.endScope()
}

var compoundOps = map[token.Type]object.Op{
	token.PLUS_ASSIGN:    object.OpAdd,
	token.MINUS_ASSIGN:   object.OpSub,
	token.STAR_ASSIGN:    object.OpMul,
	token.SLASH_ASSIGN:   object.OpDiv,
	token.PERCENT_ASSIGN: object.OpMod,
}

func (c *Compiler) assign(n *ast.AssignStmt) {
	arith, compound := compoundOps[n.Op]
	switch t := n.Target.(type) {
	case *ast.Ident:
		if compound {
			c.loadName(t.P, t.Name)
			if sop, k, ok := c.constOp(arith, n.Value); ok {
				c.line = n.P.Line
				c.emitOpU16(sop, k) // n -= 1 is GET n; SUB_CONST 1; SET n
				c.storeName(t.P, t.Name)
				return
			}
		}
		c.expr(n.Value)
		c.line = n.P.Line
		if compound {
			c.emitOp(arith)
		}
		c.storeName(t.P, t.Name)
	case *ast.IndexExpr:
		c.expr(t.X)
		c.expr(t.Index)
		if compound {
			c.emitOp(object.OpDup2)
			c.emitOp(object.OpIndexGet)
		}
		c.expr(n.Value)
		c.line = n.P.Line
		if compound {
			c.emitOp(arith)
		}
		c.emitOp(object.OpIndexSet)
	case *ast.FieldExpr:
		name := c.constant(object.Str(t.Name))
		c.expr(t.X)
		if compound {
			c.emitOp(object.OpDup)
			c.emitOpU16(object.OpGetField, name)
		}
		c.expr(n.Value)
		c.line = n.P.Line
		if compound {
			c.emitOp(arith)
		}
		c.emitOpU16(object.OpSetField, name)
	default:
		c.errorf(n.P, "cannot assign to %s", ast.ExprString(n.Target))
	}
}

// ---- expressions ----

var binaryOps = map[token.Type]object.Op{
	token.PLUS:      object.OpAdd,
	token.MINUS:     object.OpSub,
	token.STAR:      object.OpMul,
	token.SLASH:     object.OpDiv,
	token.PERCENT:   object.OpMod,
	token.STAR_STAR: object.OpPow,
	token.EQ:        object.OpEq,
	token.NEQ:       object.OpNeq,
	token.LT:        object.OpLt,
	token.LE:        object.OpLe,
	token.GT:        object.OpGt,
	token.GE:        object.OpGe,
}

func (c *Compiler) expr(e ast.Expr) {
	c.line = e.Pos().Line
	switch e.(type) {
	case *ast.UnaryExpr, *ast.BinaryExpr, *ast.CondExpr:
		if v, ok := c.fold(e); ok {
			c.emitValue(v)
			return
		}
	}
	switch n := e.(type) {
	case *ast.Ident:
		c.loadName(n.P, n.Name)
	case *ast.IntLit:
		c.emitOpU16(object.OpConst, c.constant(object.Int(n.Value)))
	case *ast.FloatLit:
		c.emitOpU16(object.OpConst, c.constant(object.Float(n.Value)))
	case *ast.StringLit:
		c.emitOpU16(object.OpConst, c.constant(object.Str(n.Value)))
	case *ast.BoolLit:
		if n.Value {
			c.emitOp(object.OpTrue)
		} else {
			c.emitOp(object.OpFalse)
		}
	case *ast.NilLit:
		c.emitOp(object.OpNil)
	case *ast.ListLit:
		if len(n.Elems) > 4096 {
			c.errorf(n.P, "list literal too long (max 4096 elements; build it with push)")
		}
		for _, el := range n.Elems {
			c.expr(el)
		}
		c.line = n.P.Line
		c.emitOpU16(object.OpList, len(n.Elems))
	case *ast.MapLit:
		if len(n.Keys) > 2048 {
			c.errorf(n.P, "map literal too long (max 2048 entries)")
		}
		for i := range n.Keys {
			c.expr(n.Keys[i])
			c.expr(n.Values[i])
		}
		c.line = n.P.Line
		c.emitOpU16(object.OpMap, len(n.Keys))
	case *ast.UnaryExpr:
		c.expr(n.X)
		c.line = n.P.Line
		if n.Op == token.MINUS {
			c.emitOp(object.OpNeg)
		} else {
			c.emitOp(object.OpNot)
		}
	case *ast.BinaryExpr:
		switch n.Op {
		case token.AND:
			// a && b: if a is falsy the result is a, otherwise b.
			c.expr(n.L)
			end := c.emitJump(object.OpJumpIfFalseKeep)
			c.emitOp(object.OpPop)
			c.expr(n.R)
			c.patchJump(end)
		case token.OR:
			c.expr(n.L)
			end := c.emitJump(object.OpJumpIfTrueKeep)
			c.emitOp(object.OpPop)
			c.expr(n.R)
			c.patchJump(end)
		default:
			op, ok := binaryOps[n.Op]
			if !ok {
				c.errorf(n.P, "internal: unknown operator %s", n.Op)
			}
			c.expr(n.L)
			if sop, k, ok := c.constOp(op, n.R); ok {
				c.line = n.P.Line
				c.emitOpU16(sop, k)
				return
			}
			c.expr(n.R)
			c.line = n.P.Line
			c.emitOp(op)
		}
	case *ast.CallExpr:
		c.expr(n.Fn)
		for _, a := range n.Args {
			c.expr(a)
		}
		c.line = n.P.Line
		c.emitOp(object.OpCall)
		c.emitByte(byte(len(n.Args)))
	case *ast.IndexExpr:
		c.expr(n.X)
		c.expr(n.Index)
		c.line = n.P.Line
		c.emitOp(object.OpIndexGet)
	case *ast.FieldExpr:
		c.expr(n.X)
		c.line = n.P.Line
		c.emitOpU16(object.OpGetField, c.constant(object.Str(n.Name)))
	case *ast.FuncLit:
		c.function(n)
	case *ast.CondExpr:
		c.expr(n.Cond)
		elseJump := c.emitJump(object.OpJumpIfFalse)
		c.expr(n.Then)
		endJump := c.emitJump(object.OpJump)
		c.patchJump(elseJump)
		c.expr(n.Else)
		c.patchJump(endJump)
	default:
		c.errorf(e.Pos(), "internal: unknown expression %T", e)
	}
}

// constOp returns the superinstruction for "x + k" or "x - k" when k is a
// constant int: ADD_CONST k / SUB_CONST k replace the pair CONST k; ADD,
// saving a dispatch and a push/pop. n - 1 is everywhere in loops and
// recursion.
func (c *Compiler) constOp(op object.Op, k ast.Expr) (object.Op, int, bool) {
	if c.opts.NoSuperinstructions || (op != object.OpAdd && op != object.OpSub) {
		return 0, 0, false
	}
	v, ok := c.fold(k)
	if !ok || v.K != object.KInt {
		return 0, 0, false
	}
	if op == object.OpAdd {
		return object.OpAddConst, c.constant(v), true
	}
	return object.OpSubConst, c.constant(v), true
}

// shadowed reports whether name resolves to a local or upvalue here. Unlike
// resolveUpvalue it has no side effects.
func (c *Compiler) shadowed(name string) bool {
	for fs := c.fs; fs != nil; fs = fs.parent {
		if resolveLocal(fs, name) >= 0 {
			return true
		}
	}
	return false
}

// emitValue emits the instruction that pushes a constant value.
func (c *Compiler) emitValue(v object.Value) {
	switch v.K {
	case object.KNil:
		c.emitOp(object.OpNil)
	case object.KBool:
		if v.AsBool() {
			c.emitOp(object.OpTrue)
		} else {
			c.emitOp(object.OpFalse)
		}
	default:
		c.emitOpU16(object.OpConst, c.constant(v))
	}
}

// fold evaluates e at compile time if it is built only from literals
// (constant folding): 4 * 2 compiles to CONST 8. It uses the VM's own
// arithmetic, so results are identical to running the code. Anything that
// would fail at run time (1 / 0, "a" - 1) is left unfolded so the error
// still happens at run time, with a traceback.
func (c *Compiler) fold(e ast.Expr) (object.Value, bool) {
	switch n := e.(type) {
	case *ast.IntLit:
		return object.Int(n.Value), true
	case *ast.FloatLit:
		return object.Float(n.Value), true
	case *ast.StringLit:
		return object.Str(n.Value), true
	case *ast.BoolLit:
		return object.Bool(n.Value), true
	case *ast.NilLit:
		return object.Nil, true
	case *ast.Ident:
		v, ok := c.consts[n.Name]
		if ok && c.shadowed(n.Name) {
			return object.Nil, false // a local of the same name hides the const
		}
		return v, ok
	case *ast.UnaryExpr:
		x, ok := c.fold(n.X)
		if !ok {
			return x, false
		}
		if n.Op == token.BANG {
			return object.Bool(!x.Truthy()), true
		}
		switch x.K {
		case object.KInt:
			return object.Int(-x.I), true
		case object.KFloat:
			return object.Float(-x.F), true
		}
	case *ast.CondExpr:
		cond, ok := c.fold(n.Cond)
		if !ok {
			return cond, false
		}
		if cond.Truthy() {
			return c.fold(n.Then)
		}
		return c.fold(n.Else)
	case *ast.BinaryExpr:
		l, ok := c.fold(n.L)
		if !ok {
			return l, false
		}
		// && and || yield the deciding operand; the right side only needs
		// to be constant if it is the one chosen.
		switch n.Op {
		case token.AND:
			if !l.Truthy() {
				return l, true
			}
			return c.fold(n.R)
		case token.OR:
			if l.Truthy() {
				return l, true
			}
			return c.fold(n.R)
		}
		r, ok := c.fold(n.R)
		if !ok {
			return r, false
		}
		switch n.Op {
		case token.EQ:
			return object.Bool(object.Equal(l, r)), true
		case token.NEQ:
			return object.Bool(!object.Equal(l, r)), true
		case token.LT, token.LE, token.GT, token.GE:
			cmp, err := vm.Compare(l, r)
			if err != nil {
				return object.Nil, false
			}
			switch n.Op {
			case token.LT:
				return object.Bool(cmp < 0), true
			case token.LE:
				return object.Bool(cmp <= 0), true
			case token.GT:
				return object.Bool(cmp > 0), true
			}
			return object.Bool(cmp >= 0), true
		}
		op, known := binaryOps[n.Op]
		if !known {
			return object.Nil, false
		}
		v, err := vm.Arith(op, l, r)
		return v, err == nil
	}
	return object.Nil, false
}

// function compiles an operation body into its own prototype and emits a
// CLOSURE instruction in the enclosing operation that creates it at run
// time, capturing any upvalues it needs.
func (c *Compiler) function(fn *ast.FuncLit) {
	name := fn.Name
	if name == "" {
		name = "<anonymous>"
	}
	fs := &funcState{
		parent: c.fs,
		proto:  &object.FunctionProto{Name: name, Arity: len(fn.Params), Chunk: &object.Chunk{}},
		depth:  1,
		consts: map[constKey]uint16{},
	}
	fs.locals = []local{{name: "", depth: 0}}
	c.fs = fs
	for _, p := range fn.Params {
		c.addLocal(fn.P, p)
	}
	for _, st := range fn.Body.Stmts {
		c.stmt(st)
	}
	for _, l := range fs.locals[1+len(fn.Params):] {
		c.warnUnused(l) // body-level locals (parameters are exempt)
	}
	// Implicit "kongroo nil" at the end of every operation.
	c.line = lastLine(fn)
	c.emitOp(object.OpNil)
	c.emitOp(object.OpReturn)

	fs.proto.Upvalues = fs.upvalues
	c.fs = fs.parent
	c.line = fn.P.Line
	c.emitOpU16(object.OpClosure, c.constant(object.ProtoConst(fs.proto)))
}

func lastLine(fn *ast.FuncLit) int {
	if n := len(fn.Body.Stmts); n > 0 {
		return fn.Body.Stmts[n-1].Pos().Line
	}
	return fn.P.Line
}
