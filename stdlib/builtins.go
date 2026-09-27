// Package stdlib provides Sern's builtin operations and the echelon module.
package stdlib

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"sern/object"
	"sern/vm"
)

// Definer is anything that can bind global names (the VM).
type Definer interface {
	DefineGlobal(name string, v object.Value)
}

var start = time.Now()

// SteinsGateDivergence is the divergence number of the Steins;Gate worldline.
const SteinsGateDivergence = 1.048596

// Install binds every builtin and the echelon module.
func Install(d Definer) {
	for _, b := range builtins() {
		d.DefineGlobal(b.Name, object.BuiltinVal(b))
	}
	d.DefineGlobal("echelon", object.NativeVal(&echelonModule{}))
}

// Names lists all builtin global names (for docs and the REPL).
func Names() []string {
	var out []string
	for _, b := range builtins() {
		out = append(out, b.Name)
	}
	return append(out, "echelon")
}

func fn(name string, arity int, f func(c object.Caller, a []object.Value) (object.Value, error)) *object.Builtin {
	return &object.Builtin{Name: name, Arity: arity, Fn: f}
}

func argErr(name string, i int, want string, got object.Value) error {
	return fmt.Errorf("argument %d of %s must be %s, not %s", i+1, name, want, got.TypeName())
}

func wantInt(name string, a []object.Value, i int) (int64, error) {
	if a[i].K != object.KInt {
		return 0, argErr(name, i, "an int", a[i])
	}
	return a[i].I, nil
}

func wantStr(name string, a []object.Value, i int) (string, error) {
	if a[i].K != object.KString {
		return "", argErr(name, i, "a string", a[i])
	}
	return a[i].AsString(), nil
}

func wantList(name string, a []object.Value, i int) (*object.List, error) {
	if a[i].K != object.KList {
		return nil, argErr(name, i, "a list", a[i])
	}
	return a[i].AsList(), nil
}

func wantMap(name string, a []object.Value, i int) (*object.Map, error) {
	if a[i].K != object.KMap {
		return nil, argErr(name, i, "a map", a[i])
	}
	return a[i].AsMap(), nil
}

func wantNum(name string, a []object.Value, i int) (float64, error) {
	if !a[i].IsNumber() {
		return 0, argErr(name, i, "a number", a[i])
	}
	return a[i].Num(), nil
}

func joinDisplay(a []object.Value) string {
	parts := make([]string, len(a))
	for i, v := range a {
		parts[i] = v.String()
	}
	return strings.Join(parts, " ")
}

func builtins() []*object.Builtin {
	return []*object.Builtin{
		fn("print", -1, func(c object.Caller, a []object.Value) (object.Value, error) {
			fmt.Fprintln(c.Stdout(), joinDisplay(a))
			return object.Nil, nil
		}),
		fn("write", -1, func(c object.Caller, a []object.Value) (object.Value, error) {
			fmt.Fprint(c.Stdout(), joinDisplay(a))
			return object.Nil, nil
		}),
		fn("len", 1, func(_ object.Caller, a []object.Value) (object.Value, error) {
			switch a[0].K {
			case object.KString:
				return object.Int(int64(len([]rune(a[0].AsString())))), nil
			case object.KList:
				return object.Int(int64(len(a[0].AsList().Items))), nil
			case object.KMap:
				return object.Int(int64(a[0].AsMap().Len())), nil
			}
			return object.Nil, fmt.Errorf("%s has no length", a[0].TypeName())
		}),
		fn("type", 1, func(_ object.Caller, a []object.Value) (object.Value, error) {
			return object.Str(a[0].TypeName()), nil
		}),
		fn("str", 1, func(_ object.Caller, a []object.Value) (object.Value, error) {
			return object.Str(a[0].String()), nil
		}),
		fn("repr", 1, func(_ object.Caller, a []object.Value) (object.Value, error) {
			return object.Str(a[0].Repr()), nil
		}),
		fn("int", 1, func(_ object.Caller, a []object.Value) (object.Value, error) {
			switch v := a[0]; v.K {
			case object.KInt:
				return v, nil
			case object.KFloat:
				return object.Int(int64(v.F)), nil
			case object.KBool:
				return object.Int(v.I), nil
			case object.KString:
				i, err := strconv.ParseInt(strings.TrimSpace(v.AsString()), 10, 64)
				if err != nil {
					return object.Nil, fmt.Errorf("cannot convert %q to int", v.AsString())
				}
				return object.Int(i), nil
			}
			return object.Nil, fmt.Errorf("cannot convert %s to int", a[0].TypeName())
		}),
		fn("float", 1, func(_ object.Caller, a []object.Value) (object.Value, error) {
			switch v := a[0]; v.K {
			case object.KInt, object.KFloat:
				return object.Float(v.Num()), nil
			case object.KString:
				f, err := strconv.ParseFloat(strings.TrimSpace(v.AsString()), 64)
				if err != nil {
					return object.Nil, fmt.Errorf("cannot convert %q to float", v.AsString())
				}
				return object.Float(f), nil
			}
			return object.Nil, fmt.Errorf("cannot convert %s to float", a[0].TypeName())
		}),

		// ---- lists ----
		fn("push", -1, func(_ object.Caller, a []object.Value) (object.Value, error) {
			if len(a) < 2 {
				return object.Nil, errors.New("push(list, value, ...) needs at least 2 arguments")
			}
			l, err := wantList("push", a, 0)
			if err != nil {
				return object.Nil, err
			}
			l.Items = append(l.Items, a[1:]...)
			return a[0], nil
		}),
		fn("pop", 1, func(_ object.Caller, a []object.Value) (object.Value, error) {
			l, err := wantList("pop", a, 0)
			if err != nil {
				return object.Nil, err
			}
			if len(l.Items) == 0 {
				return object.Nil, errors.New("pop from an empty list")
			}
			v := l.Items[len(l.Items)-1]
			l.Items = l.Items[:len(l.Items)-1]
			return v, nil
		}),
		fn("range", -1, func(_ object.Caller, a []object.Value) (object.Value, error) {
			var lo, hi, step int64 = 0, 0, 1
			for i := range a {
				if _, err := wantInt("range", a, i); err != nil {
					return object.Nil, err
				}
			}
			switch len(a) {
			case 1:
				hi = a[0].I
			case 2:
				lo, hi = a[0].I, a[1].I
			case 3:
				lo, hi, step = a[0].I, a[1].I, a[2].I
			default:
				return object.Nil, errors.New("range takes 1 to 3 arguments")
			}
			if step == 0 {
				return object.Nil, errors.New("range step cannot be 0")
			}
			var items []object.Value
			for i := lo; (step > 0 && i < hi) || (step < 0 && i > hi); i += step {
				if len(items) > 10_000_000 {
					return object.Nil, errors.New("range too large")
				}
				items = append(items, object.Int(i))
			}
			return object.ListOf(items), nil
		}),
		fn("slice", -1, func(_ object.Caller, a []object.Value) (object.Value, error) {
			if len(a) < 2 || len(a) > 3 {
				return object.Nil, errors.New("slice(x, start[, end]) takes 2 or 3 arguments")
			}
			var n int
			var runes []rune
			switch a[0].K {
			case object.KList:
				n = len(a[0].AsList().Items)
			case object.KString:
				runes = []rune(a[0].AsString())
				n = len(runes)
			default:
				return object.Nil, argErr("slice", 0, "a list or string", a[0])
			}
			clamp := func(i int64) int {
				if i < 0 {
					i += int64(n)
				}
				return int(max(0, min(i, int64(n))))
			}
			lo, err := wantInt("slice", a, 1)
			if err != nil {
				return object.Nil, err
			}
			hi := int64(n)
			if len(a) == 3 {
				if hi, err = wantInt("slice", a, 2); err != nil {
					return object.Nil, err
				}
			}
			s, e := clamp(lo), clamp(hi)
			if e < s {
				e = s
			}
			if runes != nil || a[0].K == object.KString {
				return object.Str(string(runes[s:e])), nil
			}
			return object.ListOf(append([]object.Value(nil), a[0].AsList().Items[s:e]...)), nil
		}),
		fn("contains", 2, func(_ object.Caller, a []object.Value) (object.Value, error) {
			switch a[0].K {
			case object.KList:
				for _, it := range a[0].AsList().Items {
					if object.Equal(it, a[1]) {
						return object.True, nil
					}
				}
				return object.False, nil
			case object.KString:
				s, err := wantStr("contains", a, 1)
				if err != nil {
					return object.Nil, err
				}
				return object.Bool(strings.Contains(a[0].AsString(), s)), nil
			case object.KMap:
				_, ok, err := a[0].AsMap().Get(a[1])
				return object.Bool(ok), err
			}
			return object.Nil, argErr("contains", 0, "a list, string or map", a[0])
		}),
		fn("map", 2, func(c object.Caller, a []object.Value) (object.Value, error) {
			l, err := wantList("map", a, 0)
			if err != nil {
				return object.Nil, err
			}
			out := make([]object.Value, 0, len(l.Items))
			for _, it := range l.Items {
				r, err := c.CallValue(a[1], []object.Value{it})
				if err != nil {
					return object.Nil, err
				}
				out = append(out, r)
			}
			return object.ListOf(out), nil
		}),
		fn("filter", 2, func(c object.Caller, a []object.Value) (object.Value, error) {
			l, err := wantList("filter", a, 0)
			if err != nil {
				return object.Nil, err
			}
			var out []object.Value
			for _, it := range l.Items {
				r, err := c.CallValue(a[1], []object.Value{it})
				if err != nil {
					return object.Nil, err
				}
				if r.Truthy() {
					out = append(out, it)
				}
			}
			return object.ListOf(out), nil
		}),
		fn("reduce", 3, func(c object.Caller, a []object.Value) (object.Value, error) {
			l, err := wantList("reduce", a, 0)
			if err != nil {
				return object.Nil, err
			}
			acc := a[2]
			for _, it := range l.Items {
				if acc, err = c.CallValue(a[1], []object.Value{acc, it}); err != nil {
					return object.Nil, err
				}
			}
			return acc, nil
		}),
		fn("sort", -1, func(c object.Caller, a []object.Value) (object.Value, error) {
			if len(a) < 1 || len(a) > 2 {
				return object.Nil, errors.New("sort(list[, less]) takes 1 or 2 arguments")
			}
			l, err := wantList("sort", a, 0)
			if err != nil {
				return object.Nil, err
			}
			out := append([]object.Value(nil), l.Items...)
			var sortErr error
			sort.SliceStable(out, func(i, j int) bool {
				if sortErr != nil {
					return false
				}
				if len(a) == 2 {
					r, err := c.CallValue(a[1], []object.Value{out[i], out[j]})
					if err != nil {
						sortErr = err
						return false
					}
					return r.Truthy()
				}
				cmp, err := vm.Compare(out[i], out[j])
				if err != nil {
					sortErr = err
					return false
				}
				return cmp < 0
			})
			if sortErr != nil {
				return object.Nil, sortErr
			}
			return object.ListOf(out), nil
		}),

		// ---- maps ----
		fn("keys", 1, func(_ object.Caller, a []object.Value) (object.Value, error) {
			m, err := wantMap("keys", a, 0)
			if err != nil {
				return object.Nil, err
			}
			return object.ListOf(m.Keys()), nil
		}),
		fn("values", 1, func(_ object.Caller, a []object.Value) (object.Value, error) {
			m, err := wantMap("values", a, 0)
			if err != nil {
				return object.Nil, err
			}
			return object.ListOf(m.Values()), nil
		}),
		fn("has", 2, func(_ object.Caller, a []object.Value) (object.Value, error) {
			m, err := wantMap("has", a, 0)
			if err != nil {
				return object.Nil, err
			}
			_, ok, err := m.Get(a[1])
			return object.Bool(ok), err
		}),
		fn("del", 2, func(_ object.Caller, a []object.Value) (object.Value, error) {
			m, err := wantMap("del", a, 0)
			if err != nil {
				return object.Nil, err
			}
			ok, err := m.Delete(a[1])
			return object.Bool(ok), err
		}),

		// ---- strings ----
		fn("join", 2, func(_ object.Caller, a []object.Value) (object.Value, error) {
			l, err := wantList("join", a, 0)
			if err != nil {
				return object.Nil, err
			}
			sep, err := wantStr("join", a, 1)
			if err != nil {
				return object.Nil, err
			}
			parts := make([]string, len(l.Items))
			for i, it := range l.Items {
				parts[i] = it.String()
			}
			return object.Str(strings.Join(parts, sep)), nil
		}),
		fn("split", 2, func(_ object.Caller, a []object.Value) (object.Value, error) {
			s, err := wantStr("split", a, 0)
			if err != nil {
				return object.Nil, err
			}
			sep, err := wantStr("split", a, 1)
			if err != nil {
				return object.Nil, err
			}
			var out []object.Value
			for _, p := range strings.Split(s, sep) {
				out = append(out, object.Str(p))
			}
			return object.ListOf(out), nil
		}),
		fn("upper", 1, func(_ object.Caller, a []object.Value) (object.Value, error) {
			s, err := wantStr("upper", a, 0)
			return object.Str(strings.ToUpper(s)), err
		}),
		fn("lower", 1, func(_ object.Caller, a []object.Value) (object.Value, error) {
			s, err := wantStr("lower", a, 0)
			return object.Str(strings.ToLower(s)), err
		}),
		fn("trim", 1, func(_ object.Caller, a []object.Value) (object.Value, error) {
			s, err := wantStr("trim", a, 0)
			return object.Str(strings.TrimSpace(s)), err
		}),
		fn("replace", 3, func(_ object.Caller, a []object.Value) (object.Value, error) {
			for i := range a {
				if _, err := wantStr("replace", a, i); err != nil {
					return object.Nil, err
				}
			}
			return object.Str(strings.ReplaceAll(a[0].AsString(), a[1].AsString(), a[2].AsString())), nil
		}),
		fn("pad", 2, func(_ object.Caller, a []object.Value) (object.Value, error) {
			w, err := wantInt("pad", a, 1)
			if err != nil {
				return object.Nil, err
			}
			s := a[0].String()
			if n := int(w) - len([]rune(s)); n > 0 {
				s += strings.Repeat(" ", n)
			}
			return object.Str(s), nil
		}),
		fn("fmt", -1, func(_ object.Caller, a []object.Value) (object.Value, error) {
			if len(a) == 0 {
				return object.Nil, errors.New("fmt(template, ...) needs a template")
			}
			tmpl, err := wantStr("fmt", a, 0)
			if err != nil {
				return object.Nil, err
			}
			var b strings.Builder
			next := 1
			for {
				i := strings.Index(tmpl, "{}")
				if i < 0 {
					b.WriteString(tmpl)
					break
				}
				b.WriteString(tmpl[:i])
				if next >= len(a) {
					return object.Nil, errors.New("fmt: more {} placeholders than arguments")
				}
				b.WriteString(a[next].String())
				next++
				tmpl = tmpl[i+2:]
			}
			return object.Str(b.String()), nil
		}),

		// ---- math ----
		fn("abs", 1, func(_ object.Caller, a []object.Value) (object.Value, error) {
			switch a[0].K {
			case object.KInt:
				if a[0].I < 0 {
					return object.Int(-a[0].I), nil
				}
				return a[0], nil
			case object.KFloat:
				return object.Float(math.Abs(a[0].F)), nil
			}
			return object.Nil, argErr("abs", 0, "a number", a[0])
		}),
		fn("min", -1, minmax("min", -1)),
		fn("max", -1, minmax("max", 1)),
		fn("sqrt", 1, func(_ object.Caller, a []object.Value) (object.Value, error) {
			x, err := wantNum("sqrt", a, 0)
			return object.Float(math.Sqrt(x)), err
		}),
		fn("floor", 1, func(_ object.Caller, a []object.Value) (object.Value, error) {
			x, err := wantNum("floor", a, 0)
			return object.Int(int64(math.Floor(x))), err
		}),
		fn("ceil", 1, func(_ object.Caller, a []object.Value) (object.Value, error) {
			x, err := wantNum("ceil", a, 0)
			return object.Int(int64(math.Ceil(x))), err
		}),
		fn("pow", 2, func(_ object.Caller, a []object.Value) (object.Value, error) {
			x, err := wantNum("pow", a, 0)
			if err != nil {
				return object.Nil, err
			}
			y, err := wantNum("pow", a, 1)
			if err != nil {
				return object.Nil, err
			}
			if a[0].K == object.KInt && a[1].K == object.KInt && a[1].I >= 0 {
				r, b, e := int64(1), a[0].I, a[1].I
				for e > 0 {
					if e&1 == 1 {
						r *= b
					}
					b *= b
					e >>= 1
				}
				return object.Int(r), nil
			}
			return object.Float(math.Pow(x, y)), nil
		}),
		fn("clock", 0, func(_ object.Caller, a []object.Value) (object.Value, error) {
			return object.Float(time.Since(start).Seconds()), nil
		}),

		// ---- control ----
		fn("assert", -1, func(_ object.Caller, a []object.Value) (object.Value, error) {
			if len(a) < 1 || len(a) > 2 {
				return object.Nil, errors.New("assert(cond[, message]) takes 1 or 2 arguments")
			}
			if !a[0].Truthy() {
				msg := "assertion failed"
				if len(a) == 2 {
					msg += ": " + a[1].String()
				}
				return object.Nil, errors.New(msg)
			}
			return object.Nil, nil
		}),
		fn("error", 1, func(_ object.Caller, a []object.Value) (object.Value, error) {
			return object.Nil, errors.New(a[0].String())
		}),

		// ---- lab equipment ----
		fn("divergence", 0, func(_ object.Caller, a []object.Value) (object.Value, error) {
			return object.Float(SteinsGateDivergence), nil
		}),
	}
}

func minmax(name string, sign int) func(object.Caller, []object.Value) (object.Value, error) {
	return func(_ object.Caller, a []object.Value) (object.Value, error) {
		items := a
		if len(a) == 1 && a[0].K == object.KList {
			items = a[0].AsList().Items
		}
		if len(items) == 0 {
			return object.Nil, fmt.Errorf("%s of nothing", name)
		}
		best := items[0]
		for _, it := range items[1:] {
			c, err := vm.Compare(it, best)
			if err != nil {
				return object.Nil, err
			}
			if c*sign > 0 {
				best = it
			}
		}
		return best, nil
	}
}
