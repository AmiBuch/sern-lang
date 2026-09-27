package object

import (
	"fmt"
	"io"
	"strings"
)

// Op is a bytecode instruction. Operands follow the opcode byte;
// multi-byte operands are big-endian uint16.
type Op byte

const (
	OpConst        Op = iota // u16 const index
	OpNil                    //
	OpTrue                   //
	OpFalse                  //
	OpPop                    //
	OpDup                    // a -> a a
	OpDup2                   // a b -> a b a b
	OpGetLocal               // u16 slot
	OpSetLocal               // u16 slot (pops)
	OpGetUpvalue             // u16 index
	OpSetUpvalue             // u16 index (pops)
	OpGetGlobal              // u16 global slot
	OpSetGlobal              // u16 global slot (pops)
	OpDefineGlobal           // u16 global slot (pops)
	OpCloseUpvalue           // close upvalue at top of stack, pop
	OpAdd
	OpSub
	OpMul
	OpDiv
	OpMod
	OpNeg
	OpNot
	OpEq
	OpNeq
	OpLt
	OpLe
	OpGt
	OpGe
	OpJump            // u16 forward offset
	OpJumpIfFalse     // u16 forward offset, pops condition
	OpJumpIfFalseKeep // u16 forward offset, leaves condition (for &&)
	OpJumpIfTrueKeep  // u16 forward offset, leaves condition (for ||)
	OpLoop            // u16 backward offset
	OpCall            // u8 argc
	OpClosure         // u16 const index of FunctionProto
	OpReturn
	OpList     // u16 element count
	OpMap      // u16 pair count
	OpIndexGet // x i -> x[i]
	OpIndexSet // x i v -> (pops 3)
	OpGetField // u16 name const: x -> x.name
	OpSetField // u16 name const: x v -> (pops 2)
	OpIterPrep // iterable -> iterator
	OpForIter  // u16 iterator slot, u8 vars, u16 exit offset
	OpEcho     // REPL: print value if not nil, pop
	OpPow
	OpAddConst // u16 const index: x -> x + k (superinstruction for CONST k; ADD)
	OpSubConst // u16 const index: x -> x - k
	OpTailCall // u8 argc: call that reuses the current frame, then returns
	opCount
)

type opInfo struct {
	name     string
	operands []int // byte width of each operand
}

var opTable = [opCount]opInfo{
	OpConst:           {"CONST", []int{2}},
	OpNil:             {"NIL", nil},
	OpTrue:            {"TRUE", nil},
	OpFalse:           {"FALSE", nil},
	OpPop:             {"POP", nil},
	OpDup:             {"DUP", nil},
	OpDup2:            {"DUP2", nil},
	OpGetLocal:        {"GET_LOCAL", []int{2}},
	OpSetLocal:        {"SET_LOCAL", []int{2}},
	OpGetUpvalue:      {"GET_UPVALUE", []int{2}},
	OpSetUpvalue:      {"SET_UPVALUE", []int{2}},
	OpGetGlobal:       {"GET_GLOBAL", []int{2}},
	OpSetGlobal:       {"SET_GLOBAL", []int{2}},
	OpDefineGlobal:    {"DEFINE_GLOBAL", []int{2}},
	OpCloseUpvalue:    {"CLOSE_UPVALUE", nil},
	OpAdd:             {"ADD", nil},
	OpSub:             {"SUB", nil},
	OpMul:             {"MUL", nil},
	OpDiv:             {"DIV", nil},
	OpMod:             {"MOD", nil},
	OpNeg:             {"NEG", nil},
	OpNot:             {"NOT", nil},
	OpEq:              {"EQ", nil},
	OpNeq:             {"NEQ", nil},
	OpLt:              {"LT", nil},
	OpLe:              {"LE", nil},
	OpGt:              {"GT", nil},
	OpGe:              {"GE", nil},
	OpJump:            {"JUMP", []int{2}},
	OpJumpIfFalse:     {"JUMP_IF_FALSE", []int{2}},
	OpJumpIfFalseKeep: {"JUMP_IF_FALSE_KEEP", []int{2}},
	OpJumpIfTrueKeep:  {"JUMP_IF_TRUE_KEEP", []int{2}},
	OpLoop:            {"LOOP", []int{2}},
	OpCall:            {"CALL", []int{1}},
	OpClosure:         {"CLOSURE", []int{2}},
	OpReturn:          {"RETURN", nil},
	OpList:            {"LIST", []int{2}},
	OpMap:             {"MAP", []int{2}},
	OpIndexGet:        {"INDEX_GET", nil},
	OpIndexSet:        {"INDEX_SET", nil},
	OpGetField:        {"GET_FIELD", []int{2}},
	OpSetField:        {"SET_FIELD", []int{2}},
	OpIterPrep:        {"ITER_PREP", nil},
	OpForIter:         {"FOR_ITER", []int{2, 1, 2}},
	OpEcho:            {"ECHO", nil},
	OpPow:             {"POW", nil},
	OpAddConst:        {"ADD_CONST", []int{2}},
	OpSubConst:        {"SUB_CONST", []int{2}},
	OpTailCall:        {"TAIL_CALL", []int{1}},
}

func (op Op) String() string {
	if op < opCount {
		return opTable[op].name
	}
	return fmt.Sprintf("OP_%d", byte(op))
}

// Width returns the total encoded size of an instruction, opcode included.
func (op Op) Width() int {
	w := 1
	if op < opCount {
		for _, n := range opTable[op].operands {
			w += n
		}
	}
	return w
}

// Chunk is a sequence of bytecode with its constant pool.
// Lines has one entry per code byte, for error messages.
type Chunk struct {
	Code   []byte
	Consts []Value
	Lines  []int
}

// Disassemble writes a human-readable listing of proto and every
// operation nested inside it.
func Disassemble(w io.Writer, proto *FunctionProto, globals []string) {
	name := proto.Name
	if name == "" {
		name = "<anonymous>"
	}
	fmt.Fprintf(w, "== operation %s (arity %d, upvalues %d, %d bytes, %d consts) ==\n",
		name, proto.Arity, len(proto.Upvalues), len(proto.Chunk.Code), len(proto.Chunk.Consts))
	c := proto.Chunk
	lastLine := -1
	for ip := 0; ip < len(c.Code); {
		line := "   |"
		if c.Lines[ip] != lastLine {
			line = fmt.Sprintf("%4d", c.Lines[ip])
			lastLine = c.Lines[ip]
		}
		ip = DisassembleInstr(w, c, ip, line, globals)
	}
	for _, k := range c.Consts {
		if p, ok := k.O.(*FunctionProto); ok {
			fmt.Fprintln(w)
			Disassemble(w, p, globals)
		}
	}
}

// DisassembleInstr writes the one instruction at ip, labelled with line,
// and returns the offset of the next instruction.
func DisassembleInstr(w io.Writer, c *Chunk, ip int, line string, globals []string) int {
	op := Op(c.Code[ip])
	fmt.Fprintf(w, "%04d %s  %-18s", ip, line, op)
	ops := readOperands(c.Code, ip)
	switch op {
	case OpConst, OpAddConst, OpSubConst:
		fmt.Fprintf(w, " %5d  ; %s", ops[0], c.Consts[ops[0]].Repr())
	case OpGetField, OpSetField:
		fmt.Fprintf(w, " %5d  ; .%s", ops[0], c.Consts[ops[0]].AsString())
	case OpGetGlobal, OpSetGlobal, OpDefineGlobal:
		g := "?"
		if ops[0] < len(globals) {
			g = globals[ops[0]]
		}
		fmt.Fprintf(w, " %5d  ; %s", ops[0], g)
	case OpJump, OpJumpIfFalse, OpJumpIfFalseKeep, OpJumpIfTrueKeep:
		fmt.Fprintf(w, " %5d  ; -> %04d", ops[0], ip+op.Width()+ops[0])
	case OpLoop:
		fmt.Fprintf(w, " %5d  ; -> %04d", ops[0], ip+op.Width()-ops[0])
	case OpClosure:
		p := c.Consts[ops[0]].O.(*FunctionProto)
		var ups []string
		for _, u := range p.Upvalues {
			if u.IsLocal {
				ups = append(ups, fmt.Sprintf("local %d", u.Index))
			} else {
				ups = append(ups, fmt.Sprintf("upvalue %d", u.Index))
			}
		}
		fmt.Fprintf(w, " %5d  ; <operation %s>", ops[0], p.Name)
		if len(ups) > 0 {
			fmt.Fprintf(w, " captures [%s]", strings.Join(ups, ", "))
		}
	case OpForIter:
		fmt.Fprintf(w, " slot=%d vars=%d  ; exit -> %04d", ops[0], ops[1], ip+op.Width()+ops[2])
	default:
		for _, o := range ops {
			fmt.Fprintf(w, " %5d", o)
		}
	}
	fmt.Fprintln(w)
	return ip + op.Width()
}

func readOperands(code []byte, ip int) []int {
	op := Op(code[ip])
	if op >= opCount {
		return nil
	}
	var out []int
	at := ip + 1
	for _, n := range opTable[op].operands {
		v := 0
		for i := 0; i < n; i++ {
			v = v<<8 | int(code[at+i])
		}
		out = append(out, v)
		at += n
	}
	return out
}
