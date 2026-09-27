// Package vm executes Sern bytecode on a stack machine.
//
// Each call pushes a frame whose locals live directly on the shared value
// stack (slot 0 is the callee, then the arguments, then other locals).
// Instructions pop their operands off the stack and push results.
package vm

import (
	"fmt"
	"io"
	"math"
	"strings"

	"sern/object"
)

const (
	// StackMax is the number of value slots on the stack.
	StackMax = 1 << 16
	// FramesMax bounds recursion depth.
	FramesMax = 4096
	// stackHeadroom is the space reserved for a call's temporaries.
	stackHeadroom = 2048
)

type frame struct {
	cl   *object.Closure
	ip   int
	base int
	tail int // tail calls that reused this frame (shown in tracebacks)
}

// VM is a Sern virtual machine. It is not safe for concurrent use.
type VM struct {
	stack   []object.Value // fixed length: pointers into it stay valid
	sp      int
	frames  []frame // fixed capacity: &frames[i] stays valid
	syms    *object.Symbols
	globals []object.Value
	open    *object.Upvalue // open upvalues, sorted by slot, highest first
	out     io.Writer
	// File is the source name shown in tracebacks.
	File string
	// Trace, when set, receives the current frame's stack and each
	// instruction before it executes (ibn-5100 run --trace).
	Trace io.Writer
}

// New creates a VM bound to a symbol table and output writer.
func New(syms *object.Symbols, out io.Writer) *VM {
	return &VM{
		stack:  make([]object.Value, StackMax),
		frames: make([]frame, 0, FramesMax),
		syms:   syms,
		out:    out,
	}
}

// Stdout implements object.Caller.
func (vm *VM) Stdout() io.Writer { return vm.out }

// Symbols returns the VM's global symbol table.
func (vm *VM) Symbols() *object.Symbols { return vm.syms }

func (vm *VM) syncGlobals() {
	for len(vm.globals) < len(vm.syms.Names) {
		vm.globals = append(vm.globals, object.Undef)
	}
}

// DefineGlobal binds a global name (used to install builtins).
func (vm *VM) DefineGlobal(name string, v object.Value) {
	slot := vm.syms.Define(name)
	vm.syncGlobals()
	vm.globals[slot] = v
}

// Run executes a compiled script.
func (vm *VM) Run(script *object.FunctionProto) (result object.Value, err error) {
	vm.syncGlobals()
	vm.sp = 0
	vm.frames = vm.frames[:0]
	vm.open = nil
	defer func() {
		if r := recover(); r != nil {
			// A Go runtime panic (for example indexing past the fixed stack)
			// becomes a Sern runtime error instead of crashing the host.
			result, err = object.Nil, vm.runtimeError("internal VM fault: %v", r)
			vm.sp = 0
			vm.frames = vm.frames[:0]
		}
	}()
	cl := &object.Closure{Proto: script}
	vm.push(object.ClosureVal(cl))
	if err := vm.callValue(object.ClosureVal(cl), 0); err != nil {
		return object.Nil, err
	}
	return vm.run(0)
}

// CallValue calls a Sern value from Go (used by builtins such as map and sort).
func (vm *VM) CallValue(fn object.Value, args []object.Value) (object.Value, error) {
	switch fn.K {
	case object.KBuiltin:
		b := fn.O.(*object.Builtin)
		if b.Arity >= 0 && len(args) != b.Arity {
			return object.Nil, fmt.Errorf("%s expects %d arguments, got %d", b.Name, b.Arity, len(args))
		}
		return b.Fn(vm, args)
	case object.KClosure:
		depth := len(vm.frames)
		vm.push(fn)
		for _, a := range args {
			vm.push(a)
		}
		if err := vm.callValue(fn, len(args)); err != nil {
			return object.Nil, err
		}
		return vm.run(depth)
	}
	return object.Nil, fmt.Errorf("cannot call %s: it is not an operation", fn.TypeName())
}

func (vm *VM) push(v object.Value) {
	vm.stack[vm.sp] = v
	vm.sp++
}

func (vm *VM) pop() object.Value {
	vm.sp--
	return vm.stack[vm.sp]
}

func (vm *VM) peek(dist int) object.Value { return vm.stack[vm.sp-1-dist] }

// callValue sets up a call to the value sitting below argc arguments.
func (vm *VM) callValue(callee object.Value, argc int) error {
	switch callee.K {
	case object.KClosure:
		cl := callee.AsClosure()
		if err := vm.checkArity(cl, argc); err != nil {
			return err
		}
		if len(vm.frames) == FramesMax || vm.sp+stackHeadroom > StackMax {
			return vm.runtimeError("Reading Steiner overload: stack overflow (recursion deeper than %d worldlines)", FramesMax)
		}
		vm.frames = append(vm.frames, frame{cl: cl, base: vm.sp - argc - 1})
		return nil

	case object.KBuiltin:
		b := callee.O.(*object.Builtin)
		if b.Arity >= 0 && argc != b.Arity {
			return vm.runtimeError("%s expects %d argument%s, got %d", b.Name, b.Arity, plural(b.Arity), argc)
		}
		args := make([]object.Value, argc)
		copy(args, vm.stack[vm.sp-argc:vm.sp])
		res, err := b.Fn(vm, args)
		if err != nil {
			if re, ok := err.(*RuntimeError); ok {
				return re
			}
			return vm.runtimeError("%s: %v", b.Name, err)
		}
		vm.sp -= argc + 1
		vm.push(res)
		return nil
	}
	return vm.runtimeError("cannot call %s: it is not an operation", callee.TypeName())
}

func (vm *VM) checkArity(cl *object.Closure, argc int) error {
	if argc != cl.Proto.Arity {
		return vm.runtimeError("operation %s expects %d argument%s, got %d",
			cl.Proto.Name, cl.Proto.Arity, plural(cl.Proto.Arity), argc)
	}
	return nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// capture returns the open upvalue for a stack slot, creating it if
// needed. Sharing one Upvalue per slot is what lets two closures that
// capture the same variable see each other's writes.
func (vm *VM) capture(slot int) *object.Upvalue {
	var prev *object.Upvalue
	uv := vm.open
	for uv != nil && uv.Slot > slot {
		prev, uv = uv, uv.Next
	}
	if uv != nil && uv.Slot == slot {
		return uv
	}
	n := &object.Upvalue{Loc: &vm.stack[slot], Slot: slot, Next: uv}
	if prev == nil {
		vm.open = n
	} else {
		prev.Next = n
	}
	return n
}

// closeUpvalues moves every captured variable at or above slot off the
// stack and into its Upvalue, so it outlives the frame that declared it.
func (vm *VM) closeUpvalues(slot int) {
	for vm.open != nil && vm.open.Slot >= slot {
		uv := vm.open
		uv.Closed = *uv.Loc
		uv.Loc = &uv.Closed
		vm.open = uv.Next
	}
}

// run is the dispatch loop. It returns when a RETURN brings the frame
// count back down to stopAt.
func (vm *VM) run(stopAt int) (object.Value, error) {
	// Hot state lives in local variables (registers, ideally) rather than
	// being re-read through the frame pointer on every instruction.
	fr := &vm.frames[len(vm.frames)-1]
	code := fr.cl.Proto.Chunk.Code
	consts := fr.cl.Proto.Chunk.Consts
	ip := fr.ip

	for {
		if vm.Trace != nil {
			vm.traceInstr(fr, ip)
		}
		op := object.Op(code[ip])
		ip++
		// Publish ip so errors and calls see where we are. It points just
		// past the opcode, so ip-1 maps to this instruction's source line.
		fr.ip = ip
		switch op {
		case object.OpConst:
			vm.push(consts[u16(code, ip)])
			ip += 2
		case object.OpNil:
			vm.push(object.Nil)
		case object.OpTrue:
			vm.push(object.True)
		case object.OpFalse:
			vm.push(object.False)
		case object.OpPop:
			vm.sp--
		case object.OpDup:
			vm.push(vm.peek(0))
		case object.OpDup2:
			a, b := vm.peek(1), vm.peek(0)
			vm.push(a)
			vm.push(b)

		case object.OpGetLocal:
			vm.push(vm.stack[fr.base+u16(code, ip)])
			ip += 2
		case object.OpSetLocal:
			vm.stack[fr.base+u16(code, ip)] = vm.pop()
			ip += 2
		case object.OpGetUpvalue:
			vm.push(*fr.cl.Upvalues[u16(code, ip)].Loc)
			ip += 2
		case object.OpSetUpvalue:
			*fr.cl.Upvalues[u16(code, ip)].Loc = vm.pop()
			ip += 2
		case object.OpGetGlobal:
			slot := u16(code, ip)
			ip += 2
			v := vm.globals[slot]
			if v.K == object.KUndef {
				return object.Nil, vm.runtimeError("'%s' has not been defined yet on this worldline", vm.syms.Names[slot])
			}
			vm.push(v)
		case object.OpSetGlobal:
			slot := u16(code, ip)
			ip += 2
			if vm.globals[slot].K == object.KUndef {
				return object.Nil, vm.runtimeError("cannot assign to '%s' before it is defined", vm.syms.Names[slot])
			}
			vm.globals[slot] = vm.pop()
		case object.OpDefineGlobal:
			vm.globals[u16(code, ip)] = vm.pop()
			ip += 2
		case object.OpCloseUpvalue:
			vm.closeUpvalues(vm.sp - 1)
			vm.sp--

		case object.OpAdd, object.OpSub, object.OpMul, object.OpLt, object.OpLe, object.OpGt, object.OpGe:
			// Fast path: int op int, computed in place on the stack. This is
			// by far the most common case, and skipping the generic code
			// below roughly doubles the speed of arithmetic-heavy programs.
			a, b := &vm.stack[vm.sp-2], &vm.stack[vm.sp-1]
			if a.K == object.KInt && b.K == object.KInt {
				switch op {
				case object.OpAdd:
					a.I += b.I
				case object.OpSub:
					a.I -= b.I
				case object.OpMul:
					a.I *= b.I
				case object.OpLt:
					*a = object.Bool(a.I < b.I)
				case object.OpLe:
					*a = object.Bool(a.I <= b.I)
				case object.OpGt:
					*a = object.Bool(a.I > b.I)
				case object.OpGe:
					*a = object.Bool(a.I >= b.I)
				}
				vm.sp--
				continue
			}
			if op == object.OpAdd || op == object.OpSub || op == object.OpMul {
				r, err := arith(op, *a, *b)
				if err != nil {
					return object.Nil, vm.runtimeError("%s", err)
				}
				vm.sp--
				vm.stack[vm.sp-1] = r
				continue
			}
			c, err := Compare(*a, *b)
			if err != nil {
				return object.Nil, vm.runtimeError("%s", err)
			}
			var r bool
			switch op {
			case object.OpLt:
				r = c < 0
			case object.OpLe:
				r = c <= 0
			case object.OpGt:
				r = c > 0
			default:
				r = c >= 0
			}
			vm.sp--
			vm.stack[vm.sp-1] = object.Bool(r)
		case object.OpAddConst, object.OpSubConst:
			k := consts[u16(code, ip)]
			ip += 2
			a := &vm.stack[vm.sp-1]
			if a.K == object.KInt {
				if op == object.OpAddConst {
					a.I += k.I
				} else {
					a.I -= k.I
				}
				continue
			}
			aop := object.OpAdd
			if op == object.OpSubConst {
				aop = object.OpSub
			}
			r, err := arith(aop, *a, k)
			if err != nil {
				return object.Nil, vm.runtimeError("%s", err)
			}
			*a = r
		case object.OpDiv, object.OpMod, object.OpPow:
			b := vm.pop()
			a := vm.pop()
			r, err := arith(op, a, b)
			if err != nil {
				return object.Nil, vm.runtimeError("%s", err)
			}
			vm.push(r)
		case object.OpNeg:
			a := vm.pop()
			switch a.K {
			case object.KInt:
				vm.push(object.Int(-a.I))
			case object.KFloat:
				vm.push(object.Float(-a.F))
			default:
				return object.Nil, vm.runtimeError("cannot negate %s", a.TypeName())
			}
		case object.OpNot:
			vm.push(object.Bool(!vm.pop().Truthy()))
		case object.OpEq:
			b := vm.pop()
			a := vm.pop()
			vm.push(object.Bool(object.Equal(a, b)))
		case object.OpNeq:
			b := vm.pop()
			a := vm.pop()
			vm.push(object.Bool(!object.Equal(a, b)))
		case object.OpJump:
			off := u16(code, ip)
			ip += 2
			ip += off
		case object.OpJumpIfFalse:
			off := u16(code, ip)
			ip += 2
			if !vm.pop().Truthy() {
				ip += off
			}
		case object.OpJumpIfFalseKeep:
			off := u16(code, ip)
			ip += 2
			if !vm.peek(0).Truthy() {
				ip += off
			}
		case object.OpJumpIfTrueKeep:
			off := u16(code, ip)
			ip += 2
			if vm.peek(0).Truthy() {
				ip += off
			}
		case object.OpLoop:
			off := u16(code, ip)
			ip += 2
			ip -= off

		case object.OpCall:
			argc := int(code[ip])
			ip++
			fr.ip = ip // return address
			if err := vm.callValue(vm.peek(argc), argc); err != nil {
				return object.Nil, err
			}
			fr = &vm.frames[len(vm.frames)-1]
			code, consts, ip = fr.cl.Proto.Chunk.Code, fr.cl.Proto.Chunk.Consts, fr.ip

		case object.OpTailCall:
			argc := int(code[ip])
			ip++
			fr.ip = ip
			callee := vm.peek(argc)
			if callee.K == object.KClosure {
				// Reuse this frame: close its upvalues, slide the callee and
				// arguments down over it, and start the callee at ip 0. The
				// frame count doesn't grow, so tail recursion runs in
				// constant stack space.
				cl := callee.AsClosure()
				if err := vm.checkArity(cl, argc); err != nil {
					return object.Nil, err
				}
				vm.closeUpvalues(fr.base)
				copy(vm.stack[fr.base:], vm.stack[vm.sp-argc-1:vm.sp])
				vm.sp = fr.base + argc + 1
				fr.cl, fr.ip = cl, 0
				fr.tail++
				code, consts, ip = cl.Proto.Chunk.Code, cl.Proto.Chunk.Consts, 0
				continue
			}
			// A builtin runs to completion immediately: call it, then
			// return its result exactly like RETURN.
			if err := vm.callValue(callee, argc); err != nil {
				return object.Nil, err
			}
			res := vm.pop()
			vm.closeUpvalues(fr.base)
			vm.sp = fr.base
			vm.frames = vm.frames[:len(vm.frames)-1]
			if len(vm.frames) == stopAt {
				return res, nil
			}
			vm.push(res)
			fr = &vm.frames[len(vm.frames)-1]
			code, consts, ip = fr.cl.Proto.Chunk.Code, fr.cl.Proto.Chunk.Consts, fr.ip

		case object.OpClosure:
			proto := consts[u16(code, ip)].O.(*object.FunctionProto)
			ip += 2
			cl := &object.Closure{Proto: proto, Upvalues: make([]*object.Upvalue, len(proto.Upvalues))}
			for i, u := range proto.Upvalues {
				if u.IsLocal {
					cl.Upvalues[i] = vm.capture(fr.base + int(u.Index))
				} else {
					cl.Upvalues[i] = fr.cl.Upvalues[u.Index]
				}
			}
			vm.push(object.ClosureVal(cl))

		case object.OpReturn:
			res := vm.pop()
			vm.closeUpvalues(fr.base)
			vm.sp = fr.base
			vm.frames = vm.frames[:len(vm.frames)-1]
			if len(vm.frames) == stopAt {
				return res, nil
			}
			vm.push(res)
			fr = &vm.frames[len(vm.frames)-1]
			code, consts, ip = fr.cl.Proto.Chunk.Code, fr.cl.Proto.Chunk.Consts, fr.ip

		case object.OpList:
			n := u16(code, ip)
			ip += 2
			items := make([]object.Value, n)
			copy(items, vm.stack[vm.sp-n:vm.sp])
			vm.sp -= n
			vm.push(object.ListOf(items))
		case object.OpMap:
			n := u16(code, ip)
			ip += 2
			m := object.NewMap()
			base := vm.sp - 2*n
			for i := 0; i < n; i++ {
				if err := m.Set(vm.stack[base+2*i], vm.stack[base+2*i+1]); err != nil {
					return object.Nil, vm.runtimeError("%s", err)
				}
			}
			vm.sp = base
			vm.push(object.MapVal(m))

		case object.OpIndexGet:
			idx := vm.pop()
			x := vm.pop()
			v, err := indexGet(x, idx)
			if err != nil {
				return object.Nil, vm.runtimeError("%s", err)
			}
			vm.push(v)
		case object.OpIndexSet:
			val := vm.pop()
			idx := vm.pop()
			x := vm.pop()
			if err := indexSet(x, idx, val); err != nil {
				return object.Nil, vm.runtimeError("%s", err)
			}
		case object.OpGetField:
			name := consts[u16(code, ip)].AsString()
			ip += 2
			x := vm.pop()
			v, err := getField(x, name)
			if err != nil {
				return object.Nil, vm.runtimeError("%s", err)
			}
			vm.push(v)
		case object.OpSetField:
			name := consts[u16(code, ip)].AsString()
			ip += 2
			val := vm.pop()
			x := vm.pop()
			if x.K != object.KMap {
				return object.Nil, vm.runtimeError("cannot set field .%s on %s", name, x.TypeName())
			}
			x.AsMap().SetStr(name, val)

		case object.OpIterPrep:
			it, err := object.NewIter(vm.pop())
			if err != nil {
				return object.Nil, vm.runtimeError("%s", err)
			}
			vm.push(object.Value{K: object.KIter, O: it})
		case object.OpForIter:
			slot := u16(code, ip)
			ip += 2
			vars := int(code[ip])
			ip++
			exit := u16(code, ip)
			ip += 2
			it := vm.stack[fr.base+slot].O.(*object.Iter)
			first, second, ok := it.Next()
			if !ok {
				ip += exit
				break
			}
			switch {
			case vars == 2:
				vm.push(first)
				vm.push(second)
			case it.IsMap():
				vm.push(first) // single variable over a map binds the key
			default:
				vm.push(second) // ... over a list/string binds the element
			}

		case object.OpEcho:
			if v := vm.pop(); v.K != object.KNil {
				fmt.Fprintln(vm.out, v.Repr())
			}

		default:
			return object.Nil, vm.runtimeError("unknown opcode %d (corrupt bytecode?)", op)
		}
	}
}

// traceInstr prints the current frame's stack slots, then the instruction
// about to run:
//
//	          [ <operation fib> | 25 | 24 ]
//	fib       0007   2  LT
func (vm *VM) traceInstr(fr *frame, ip int) {
	var b strings.Builder
	b.WriteString("          [")
	for i := fr.base; i < vm.sp; i++ {
		if i > fr.base {
			b.WriteString(" |")
		}
		b.WriteString(" " + vm.stack[i].Repr())
	}
	b.WriteString(" ]\n")
	name := fr.cl.Proto.Name
	if len(name) > 9 {
		name = name[:8] + "…"
	}
	fmt.Fprintf(&b, "%-9s ", name)
	io.WriteString(vm.Trace, b.String())
	c := fr.cl.Proto.Chunk
	object.DisassembleInstr(vm.Trace, c, ip, fmt.Sprintf("%4d", c.Lines[ip]), vm.syms.Names)
}

// u16 decodes a big-endian operand. Small enough for Go to inline.
func u16(code []byte, ip int) int { return int(code[ip])<<8 | int(code[ip+1]) }

// ---- helpers shared with the stdlib ----

// Arith applies an arithmetic opcode (ADD, SUB, MUL, DIV, MOD, POW) to two
// values with the VM's exact semantics. The compiler uses it for constant
// folding, so folded and unfolded code can never disagree.
func Arith(op object.Op, a, b object.Value) (object.Value, error) { return arith(op, a, b) }

func arith(op object.Op, a, b object.Value) (object.Value, error) {
	if op == object.OpAdd {
		switch {
		case a.K == object.KString || b.K == object.KString:
			return object.Str(a.String() + b.String()), nil
		case a.K == object.KList && b.K == object.KList:
			x, y := a.AsList().Items, b.AsList().Items
			out := make([]object.Value, 0, len(x)+len(y))
			return object.ListOf(append(append(out, x...), y...)), nil
		}
	}
	if !a.IsNumber() || !b.IsNumber() {
		return object.Nil, fmt.Errorf("cannot apply %s to %s and %s", opSymbol(op), a.TypeName(), b.TypeName())
	}
	if a.K == object.KInt && b.K == object.KInt {
		x, y := a.I, b.I
		switch op {
		case object.OpPow:
			if y >= 0 {
				return object.Int(ipow(x, y)), nil
			}
			// A negative exponent can't stay an int: 2 ** -1 is 0.5.
		case object.OpAdd:
			return object.Int(x + y), nil
		case object.OpSub:
			return object.Int(x - y), nil
		case object.OpMul:
			return object.Int(x * y), nil
		case object.OpDiv:
			if y == 0 {
				return object.Nil, fmt.Errorf("division by zero")
			}
			return object.Int(x / y), nil
		case object.OpMod:
			if y == 0 {
				return object.Nil, fmt.Errorf("modulo by zero")
			}
			return object.Int(x % y), nil
		}
	}
	x, y := a.Num(), b.Num()
	switch op {
	case object.OpAdd:
		return object.Float(x + y), nil
	case object.OpSub:
		return object.Float(x - y), nil
	case object.OpMul:
		return object.Float(x * y), nil
	case object.OpDiv:
		if y == 0 {
			return object.Nil, fmt.Errorf("division by zero")
		}
		return object.Float(x / y), nil
	case object.OpMod:
		if y == 0 {
			return object.Nil, fmt.Errorf("modulo by zero")
		}
		return object.Float(math.Mod(x, y)), nil
	case object.OpPow:
		return object.Float(math.Pow(x, y)), nil
	}
	return object.Nil, fmt.Errorf("unknown arithmetic op")
}

// ipow computes x**y for y >= 0 by repeated squaring. Like the other int
// operators it wraps on overflow.
func ipow(x, y int64) int64 {
	r := int64(1)
	for y > 0 {
		if y&1 == 1 {
			r *= x
		}
		x *= x
		y >>= 1
	}
	return r
}

func opSymbol(op object.Op) string {
	switch op {
	case object.OpAdd:
		return "'+'"
	case object.OpSub:
		return "'-'"
	case object.OpMul:
		return "'*'"
	case object.OpDiv:
		return "'/'"
	case object.OpMod:
		return "'%'"
	case object.OpPow:
		return "'**'"
	}
	return op.String()
}

// Compare orders numbers and strings. It is exported for sort().
func Compare(a, b object.Value) (int, error) {
	switch {
	case a.K == object.KInt && b.K == object.KInt:
		return cmp3(a.I < b.I, a.I > b.I), nil
	case a.IsNumber() && b.IsNumber():
		x, y := a.Num(), b.Num()
		return cmp3(x < y, x > y), nil
	case a.K == object.KString && b.K == object.KString:
		return strings.Compare(a.AsString(), b.AsString()), nil
	}
	return 0, fmt.Errorf("cannot compare %s with %s", a.TypeName(), b.TypeName())
}

func cmp3(lt, gt bool) int {
	switch {
	case lt:
		return -1
	case gt:
		return 1
	}
	return 0
}

func normIndex(i object.Value, n int) (int, error) {
	if i.K != object.KInt {
		return 0, fmt.Errorf("index must be an int, not %s", i.TypeName())
	}
	idx := i.I
	if idx < 0 {
		idx += int64(n)
	}
	if idx < 0 || idx >= int64(n) {
		return 0, fmt.Errorf("index %d is outside this worldline (length %d)", i.I, n)
	}
	return int(idx), nil
}

func indexGet(x, i object.Value) (object.Value, error) {
	switch x.K {
	case object.KList:
		items := x.AsList().Items
		idx, err := normIndex(i, len(items))
		if err != nil {
			return object.Nil, err
		}
		return items[idx], nil
	case object.KMap:
		v, _, err := x.AsMap().Get(i)
		return v, err
	case object.KString:
		rs := []rune(x.AsString())
		idx, err := normIndex(i, len(rs))
		if err != nil {
			return object.Nil, err
		}
		return object.Str(string(rs[idx])), nil
	}
	return object.Nil, fmt.Errorf("cannot index into %s", x.TypeName())
}

func indexSet(x, i, v object.Value) error {
	switch x.K {
	case object.KList:
		items := x.AsList().Items
		idx, err := normIndex(i, len(items))
		if err != nil {
			return err
		}
		items[idx] = v
		return nil
	case object.KMap:
		return x.AsMap().Set(i, v)
	}
	return fmt.Errorf("cannot assign into %s by index", x.TypeName())
}

func getField(x object.Value, name string) (object.Value, error) {
	switch x.K {
	case object.KMap:
		v, _ := x.AsMap().GetStr(name)
		return v, nil
	case object.KNative:
		n := x.O.(object.Native)
		if v, ok := n.GetField(name); ok {
			return v, nil
		}
		return object.Nil, fmt.Errorf("%s has no field '%s'", n.TypeName(), name)
	}
	return object.Nil, fmt.Errorf("cannot read field '%s' of %s", name, x.TypeName())
}
