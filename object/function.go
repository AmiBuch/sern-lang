package object

import "io"

// UpvalueDesc tells OpClosure where to find a captured variable: in the
// enclosing frame's locals (IsLocal) or in the enclosing closure's upvalues.
type UpvalueDesc struct {
	IsLocal bool
	Index   uint16
}

// FunctionProto is the compiled form of an operation.
type FunctionProto struct {
	Name     string
	Arity    int
	Upvalues []UpvalueDesc
	Chunk    *Chunk
}

// Upvalue is a captured variable. While open, Loc points into the VM
// stack; when the frame exits it is closed and Loc points at Closed.
type Upvalue struct {
	Loc    *Value
	Closed Value
	Slot   int
	Next   *Upvalue
}

// Closure is a function prototype plus its captured environment.
type Closure struct {
	Proto    *FunctionProto
	Upvalues []*Upvalue
}

// Caller is the interface builtins use to call back into the VM.
type Caller interface {
	CallValue(fn Value, args []Value) (Value, error)
	Stdout() io.Writer
}

// Builtin is a function implemented in Go. Arity < 0 means variadic.
type Builtin struct {
	Name  string
	Arity int
	Fn    func(c Caller, args []Value) (Value, error)
}

// Native is a Go object exposed to Sern (for example an ECHELON cluster).
// Field access on it goes through GetField.
type Native interface {
	TypeName() string
	String() string
	GetField(name string) (Value, bool)
}

// Symbols is the global name table shared by the compiler and the VM.
// The compiler resolves global names to slots at compile time, so the VM
// indexes a slice instead of hashing names at run time.
type Symbols struct {
	Index   map[string]int
	Names   []string
	Defined []bool
}

// NewSymbols creates an empty table.
func NewSymbols() *Symbols { return &Symbols{Index: map[string]int{}} }

// Slot returns the slot for name, creating it if needed.
func (s *Symbols) Slot(name string) int {
	if i, ok := s.Index[name]; ok {
		return i
	}
	i := len(s.Names)
	s.Index[name] = i
	s.Names = append(s.Names, name)
	s.Defined = append(s.Defined, false)
	return i
}

// Define marks name as defined and returns its slot.
func (s *Symbols) Define(name string) int {
	i := s.Slot(name)
	s.Defined[i] = true
	return i
}
