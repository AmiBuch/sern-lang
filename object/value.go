// Package object defines Sern's runtime values, bytecode chunks and the
// .sernbc program file format shared by the compiler and the VM.
package object

import (
	"math"
	"strconv"
	"strings"
)

// Kind is the dynamic type tag of a Value.
type Kind uint8

const (
	KNil Kind = iota
	KBool
	KInt
	KFloat
	KString
	KList
	KMap
	KClosure
	KBuiltin
	KNative
	KIter  // internal: iterator state for reading loops
	KUndef // internal: marks a global slot that has not been defined yet
	KProto // internal: a FunctionProto in a constant pool
)

// Value is a tagged union. Numbers live inline (no allocation); strings,
// lists, maps and functions live in O. This layout maps 1:1 onto the
// tagged-union struct the LLVM backend will use in phase 2.
type Value struct {
	K Kind
	I int64
	F float64
	O any
}

var (
	Nil   = Value{K: KNil}
	True  = Value{K: KBool, I: 1}
	False = Value{K: KBool, I: 0}
	Undef = Value{K: KUndef}
)

func Bool(b bool) Value {
	if b {
		return True
	}
	return False
}
func Int(i int64) Value           { return Value{K: KInt, I: i} }
func Float(f float64) Value       { return Value{K: KFloat, F: f} }
func Str(s string) Value          { return Value{K: KString, O: s} }
func ListOf(items []Value) Value  { return Value{K: KList, O: &List{Items: items}} }
func MapVal(m *Map) Value         { return Value{K: KMap, O: m} }
func ClosureVal(c *Closure) Value { return Value{K: KClosure, O: c} }
func BuiltinVal(b *Builtin) Value { return Value{K: KBuiltin, O: b} }
func NativeVal(n Native) Value    { return Value{K: KNative, O: n} }

func (v Value) AsString() string    { return v.O.(string) }
func (v Value) AsList() *List       { return v.O.(*List) }
func (v Value) AsMap() *Map         { return v.O.(*Map) }
func (v Value) AsClosure() *Closure { return v.O.(*Closure) }
func (v Value) AsBool() bool        { return v.I != 0 }

// IsNumber reports whether v is an int or a float.
func (v Value) IsNumber() bool { return v.K == KInt || v.K == KFloat }

// Num returns v as a float64 (v must be a number).
func (v Value) Num() float64 {
	if v.K == KInt {
		return float64(v.I)
	}
	return v.F
}

// Truthy: only nil and false are falsy (Lua semantics).
func (v Value) Truthy() bool {
	return !(v.K == KNil || (v.K == KBool && v.I == 0))
}

// TypeName is what type() returns in Sern.
func (v Value) TypeName() string {
	switch v.K {
	case KNil:
		return "nil"
	case KBool:
		return "bool"
	case KInt:
		return "int"
	case KFloat:
		return "float"
	case KString:
		return "string"
	case KList:
		return "list"
	case KMap:
		return "map"
	case KClosure:
		return "operation"
	case KBuiltin:
		return "builtin"
	case KNative:
		return v.O.(Native).TypeName()
	case KIter:
		return "iterator"
	case KUndef:
		return "undefined"
	case KProto:
		return "proto"
	}
	return "?"
}

// String is the display form used by print(): strings appear raw.
func (v Value) String() string { return v.format(false, 0) }

// Repr is the literal-like form: strings are quoted.
func (v Value) Repr() string { return v.format(true, 0) }

const maxPrintDepth = 32

func (v Value) format(quote bool, depth int) string {
	if depth > maxPrintDepth {
		return "..."
	}
	switch v.K {
	case KNil:
		return "nil"
	case KBool:
		return strconv.FormatBool(v.I != 0)
	case KInt:
		return strconv.FormatInt(v.I, 10)
	case KFloat:
		return FormatFloat(v.F)
	case KString:
		if quote {
			return strconv.Quote(v.O.(string))
		}
		return v.O.(string)
	case KList:
		items := v.AsList().Items
		parts := make([]string, len(items))
		for i, it := range items {
			parts[i] = it.format(true, depth+1)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case KMap:
		m := v.AsMap()
		parts := make([]string, len(m.keys))
		for i := range m.keys {
			parts[i] = m.keys[i].format(true, depth+1) + ": " + m.vals[i].format(true, depth+1)
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case KClosure:
		name := v.AsClosure().Proto.Name
		if name == "" {
			name = "<anonymous>"
		}
		return "<operation " + name + ">"
	case KBuiltin:
		return "<builtin " + v.O.(*Builtin).Name + ">"
	case KNative:
		return v.O.(Native).String()
	case KIter:
		return "<iterator>"
	case KUndef:
		return "<undefined>"
	case KProto:
		return "<proto " + v.O.(*FunctionProto).Name + ">"
	}
	return "<?>"
}

// FormatFloat always shows a decimal point for whole floats (3.0, not 3).
func FormatFloat(f float64) string {
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return s
	}
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return s
}

// Equal compares values. Numbers compare numerically across int/float,
// lists and maps compare structurally, functions and natives by identity.
func Equal(a, b Value) bool { return equal(a, b, 0) }

func equal(a, b Value, depth int) bool {
	if depth > maxPrintDepth {
		return false
	}
	if a.IsNumber() && b.IsNumber() {
		if a.K == KInt && b.K == KInt {
			return a.I == b.I
		}
		return a.Num() == b.Num()
	}
	if a.K != b.K {
		return false
	}
	switch a.K {
	case KNil:
		return true
	case KBool:
		return a.I == b.I
	case KString:
		return a.O.(string) == b.O.(string)
	case KList:
		x, y := a.AsList().Items, b.AsList().Items
		if len(x) != len(y) {
			return false
		}
		for i := range x {
			if !equal(x[i], y[i], depth+1) {
				return false
			}
		}
		return true
	case KMap:
		x, y := a.AsMap(), b.AsMap()
		if x == y {
			return true
		}
		if x.Len() != y.Len() {
			return false
		}
		for i, k := range x.keys {
			ov, ok, _ := y.Get(k)
			if !ok || !equal(x.vals[i], ov, depth+1) {
				return false
			}
		}
		return true
	default:
		return a.O == b.O
	}
}
