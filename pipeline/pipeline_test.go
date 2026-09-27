package pipeline

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sern/compiler"
	"sern/object"
)

var update = flag.Bool("update", false, "rewrite golden files in examples/expected")

func run(t *testing.T, src string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := Run("test.sern", src, &out)
	return out.String(), err
}

func TestLanguage(t *testing.T) {
	cases := []struct {
		name, src, want string
	}{
		{"arithmetic", `print(1 + 2 * 3, (1 + 2) * 3, 7 / 2, 7 % 3, 7.0 / 2, -2 - -3)`, "7 9 3 1 3.5 1\n"},
		{"precedence", `print(1 < 2 == true, !false && 1 > 2 || 3 >= 3)`, "true true\n"},
		{"strings", `print("a" + 1 + 2.5 + nil, len("Tuturu"), upper("z"), "ab"[1], slice("hello", 1, -1))`, "a12.5nil 6 Z b ell\n"},
		{"hex and binary", `print(0xFF, 0b1010, 0x_ff, 0XaB, 0b1111_0000, 010)`, "255 10 255 171 240 10\n"},
		{"interpolation", `
labmem id = 4
print("Lab Mem {id}", "{1}{2}", "x{ {a: 1}.a + 1 }y", "n {"in {id}"}", fmt("{} ok", 1), "\{id}")`, "Lab Mem 4 12 x2y n in 4 1 ok {id}\n"},
		{"unicode identifiers", "labmem 岡部 = \"Okabe\"; labmem λ = operation(x) { kongroo x * 2 }; print(岡部, λ(21))", "Okabe 42\n"},
		{"exponent", `print(2 ** 10, 2 ** 3 ** 2, -2 ** 2, (-2) ** 3, 2 ** -1, 2.0 ** 0.5 > 1.41, 3 * 2 ** 2)`, "1024 512 -4 -8 0.5 true 12\n"},
		{"ternary", `
operation sign(n) { kongroo n < 0 ? "neg" : n == 0 ? "zero" : "pos" }
labmem hits = 0
operation touch() { hits += 1; kongroo 9 }
print(sign(-3), sign(0), sign(5), true ? 1 : touch(), false ? touch() : 2, hits, 1 || false ? "a" : "b")`, "neg zero pos 1 2 0 a\n"},
		{"const", `
const N = 3
const M = N * 2 + 1
operation f() { kongroo N + M + LATER }
const LATER = 100
operation shadow(N) { kongroo N }
print(f(), N, M, shadow(5), "N={N}")`, "110 3 7 5 N=3\n"},
		{"match", `
operation kind(x) {
    match x {
        1 => kongroo "one",
        "a" => kongroo "letter a"
        [1, 2] => {
            labmem n = len(x)
            kongroo "list of " + n
        }
        _ => kongroo "other"
    }
}
labmem out = []
reading v in [1, 2, 3, 4] {
    match v % 2 { 0 => continue, _ => { labmem half = v; push(out, half) } }
    match v { 3 => break }
}
match 5 { 1 => print("never") }
print(kind(1), kind("a"), kind([1, 2]), kind(nil), out)`, "one letter a list of 2 other [1, 3]\n"},
		{"tail calls run in constant stack", `
operation sum(n, acc) { if n == 0 { kongroo acc } kongroo sum(n - 1, acc + n) }
operation isEven(n) { if n == 0 { kongroo true } kongroo isOdd(n - 1) }
operation isOdd(n) { if n == 0 { kongroo false } kongroo isEven(n - 1) }
operation twice(n) { labmem x = n * 2; kongroo id(operation() { kongroo x }) }
operation id(v) { kongroo v }
operation count(xs) { kongroo len(xs) }
print(sum(100000, 0), isEven(10001), twice(21)(), count([1, 2]))`, "5000050000 false 42 2\n"},
		{"truthiness", `print(!nil, !0, !"", !false)`, "true false false true\n"},
		{"short circuit", `
labmem hits = 0
operation touch() { hits += 1; kongroo true }
labmem a = false && touch()
labmem b = true || touch()
print(a, b, hits)`, "false true 0\n"},
		{"globals and forward refs", `
operation isEven(n) { if n == 0 { kongroo true } kongroo isOdd(n - 1) }
operation isOdd(n) { if n == 0 { kongroo false } kongroo isEven(n - 1) }
print(isEven(10), isOdd(7))`, "true true\n"},
		{"shadowing", `
labmem x = "global"
{
    labmem x = x + "-inner"
    print(x)
}
print(x)`, "global-inner\nglobal\n"},
		{"closures share variables", `
operation pair() {
    labmem n = 0
    kongroo [operation() { n += 1 }, operation() { kongroo n }]
}
labmem p = pair()
p[0](); p[0]()
print(p[1]())`, "2\n"},
		{"nested closures", `
operation outer() {
    labmem x = 1
    operation middle() {
        operation inner() { x = x * 10; kongroo x }
        kongroo inner
    }
    kongroo middle()
}
labmem f = outer()
f()
print(f())`, "100\n"},
		{"local recursion", `
operation main() {
    operation fact(n) { if n <= 1 { kongroo 1 } kongroo n * fact(n - 1) }
    print(fact(10))
}`, "3628800\n"},
		{"loop closures capture per iteration", `
labmem fs = []
reading i in range(3) { push(fs, operation() { kongroo i }) }
print(map(fs, operation(f) { kongroo f() }))`, "[0, 1, 2]\n"},
		{"break and continue", `
labmem out = []
reading i in range(10) {
    if i == 7 { break }
    if i % 2 == 1 { continue }
    labmem sq = i * i
    push(out, sq)
}
labmem n = 0
timeleap true { n += 1; if n < 5 { continue } break }
print(out, n)`, "[0, 4, 16, 36] 5\n"},
		{"reading over maps and strings", `
labmem m = {b: 2, a: 1}
reading k in m { write(k) }
reading k, v in m { write(k, v, "") }
reading i, c in "Z!" { write(i, c, "") }
print()`, "bab 2 a 1 0 Z 1 ! \n"},
		{"maps", `
labmem m = {}
m.x = 1
m["y"] = 2
m.x += 10
print(m, m.missing, keys(m), has(m, "x"), len(m))
del(m, "x")
print(m)`, "{\"x\": 11, \"y\": 2} nil [\"x\", \"y\"] true 2\n{\"y\": 2}\n"},
		{"list ops", `
labmem xs = [3, 1, 2]
xs[0] = 5
xs[-1] += 1
print(xs, sort(xs))
print(pop(xs), xs + [9], contains(xs, 5))
print(sort(["b", "a"], operation(a, b) { kongroo a > b }), reduce(range(1, 5), operation(a, b) { kongroo a * b }, 1))`,
			"[5, 1, 3] [1, 3, 5]\n3 [5, 1, 9] true\n[\"b\", \"a\"] 24\n"},
		{"equality", `print([1, [2]] == [1, [2]], {a: 1} == {a: 1}, 1 == 1.0, "1" == 1)`, "true true true false\n"},
		{"numbers", `print(int("42") + 1, float(3), int(3.9), pow(2, 10), sqrt(16), floor(-1.5), abs(-3), min(3, 1, 2), max([4, 9]))`,
			"43 3.0 3 1024 4.0 -2 3 1 9\n"},
		{"fmt and str", `print(fmt("{}/{} = {}", 1, 2, 0.5), str([1, "a"]), repr("q"), type(nil), type(print))`,
			"1/2 = 0.5 [1, \"a\"] \"q\" nil builtin\n"},
		{"semicolons optional", "labmem a = 1; labmem b = 2\nprint(a + b)\n", "3\n"},
		{"newline ends call chain", "labmem f = print\nf(\"x\")\n(1 + 1)\n", "x\n"},
		{"kongroo without value", `
operation f(x) {
    if x { kongroo }
    kongroo "no"
}
print(f(true), f(false))`, "nil no\n"},
		{"echelon round trip", `
labmem db = echelon.cluster({nodes: 3, seed: 1})
db.put("k", {v: [1, 2.5, "s", nil, true]})
labmem r = db.get("k")
print(r.found, r.worldlines, type(r.context))`, "true [{\"v\": [1, 2.5, \"s\", nil, true]}] context\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := run(t, tc.src)
			if err != nil {
				t.Fatalf("error: %s", FormatError("test.sern", tc.src, err))
			}
			if got != tc.want {
				t.Fatalf("got:\n%q\nwant:\n%q", got, tc.want)
			}
		})
	}
}

func TestErrors(t *testing.T) {
	cases := []struct {
		name, src, want string
	}{
		{"undefined global", `print(nope)`, "'nope' is not defined"},
		{"labmem needs value", `labmem x`, "every lab member needs an initial value"},
		{"redeclare", "{ labmem a = 1\nlabmem a = 2 }", "already declared"},
		{"break outside loop", `break`, "outside of a timeleap"},
		{"bad assign", `1 = 2`, "cannot assign"},
		{"missing separator", `labmem a = 1 labmem b = 2`, "expected ';' or a newline"},
		{"unclosed block", "operation f() {\n print(1)\n", "unclosed '{'"},
		{"bad string", "print(\"abc)", "unterminated string"},
		{"empty hex", `print(0x)`, "expected hex digits after 0x"},
		{"bad binary digit", `print(0b102)`, "invalid character '2' in number"},
		{"hex overflow", `print(0xFFFFFFFFFFFFFFFF)`, "does not fit in 64 bits"},
		{"unclosed interpolation", "print(\"a {x)\nprint(1)", "unterminated string"},
		{"interpolation missing brace", `print("a {1 2}")`, "expected '}' to close interpolation"},
		{"unicode number suffix", "print(1é)", "invalid character 'é' in number"},
		{"ternary needs colon", `print(true ? 1)`, "expected ':' in 'cond ? a : b'"},
		{"pow type error", `print("a" ** 2)`, "cannot apply '**' to string and int"},
		{"assign const", "const N = 1\nN = 2", "cannot assign to 'N': it is a const"},
		{"const needs constant", "labmem x = 1\nconst C = x + 1", "must be a compile-time constant"},
		{"const at top level only", "operation f() { const C = 1 }", "only allowed at the top level"},
		{"const redeclared", "const N = 1\nlabmem N = 2", "'N' is a const and cannot be redeclared"},
		{"const hides builtin", "const print = 1", "would hide an existing global or builtin"},
		{"match arm after wildcard", "match 1 { _ => print(1), 2 => print(2) }", "unreachable match arm"},
		{"match arrow", "match 1 { 1 print(1) }", "expected '=>' after match pattern"},
		{"division by zero", `print(1 / 0)`, "division by zero"},
		{"arity", `operation f(a) {} f(1, 2)`, "expects 1 argument, got 2"},
		{"type error", `print(1 + [])`, "cannot apply '+' to int and list"},
		{"index", `print([1][5])`, "outside this worldline"},
		{"call non-function", `labmem x = 3; x()`, "not an operation"},
		{"use before define", "print(later)\nlabmem later = 1", "has not been defined yet"},
		{"stack overflow", `operation f(n) { kongroo 1 + f(n + 1) } f(0)`, "Reading Steiner overload"},
		{"assert", `assert(1 == 2, "worldlines differ")`, "worldlines differ"},
		{"error in callback", `map([1], operation(x) { kongroo x / 0 })`, "division by zero"},
		{"cannot dmail a function", `echelon.cluster().put("k", print)`, "cannot D-Mail"},
		{"bad cluster config", `echelon.cluster({nodes: 2, n: 3})`, "n must be between"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := run(t, tc.src)
			if err == nil {
				t.Fatal("expected an error")
			}
			msg := FormatError("test.sern", tc.src, err)
			if !strings.Contains(msg, tc.want) {
				t.Fatalf("error %q does not mention %q", msg, tc.want)
			}
		})
	}
}

func TestCaretAfterUnicode(t *testing.T) {
	src := "labmem ñ = 1 +"
	_, err := run(t, src)
	msg := FormatError("test.sern", src, err)
	if !strings.HasSuffix(msg, "|               ^") {
		t.Fatalf("caret misplaced:\n%s", msg)
	}
}

func TestTracebackAndCaret(t *testing.T) {
	// "inner() + 1" rather than "inner()": a plain call keeps outer's frame
	// (see TestTailCalls for what a tail call does to the traceback).
	src := "operation inner() {\n    kongroo 1 / 0\n}\noperation outer() {\n    kongroo inner() + 1\n}\nouter()\n"
	_, err := run(t, src)
	msg := FormatError("t.sern", src, err)
	for _, want := range []string{"t.sern:7 in <script>", "t.sern:5 in outer", "t.sern:2 in inner", "El Psy Kongroo."} {
		if !strings.Contains(msg, want) {
			t.Errorf("traceback missing %q:\n%s", want, msg)
		}
	}
	src = "labmem x = (1 +\n"
	_, err = run(t, src)
	if msg := FormatError("t.sern", src, err); !strings.Contains(msg, "IBN 5100 cannot decode t.sern:2:1") {
		t.Errorf("unexpected compile error format:\n%s", msg)
	}
}

// TestBytecodeRoundTrip compiles every example, writes it as .sernbc, reads it
// back and checks it produces identical output.
func TestBytecodeRoundTrip(t *testing.T) {
	files, _ := filepath.Glob("../examples/*.sern")
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			src, _ := os.ReadFile(f)
			syms, _ := NewRuntime(&bytes.Buffer{})
			proto, err := Compile(string(src), syms, compiler.Options{})
			if err != nil {
				t.Fatal(err)
			}
			var bin bytes.Buffer
			if err := object.WriteProgram(&bin, &object.Program{Source: f, Globals: syms.Names, Script: proto}); err != nil {
				t.Fatal(err)
			}
			prog, err := object.ReadProgram(&bin)
			if err != nil {
				t.Fatal(err)
			}
			var direct, loaded bytes.Buffer
			if err := Run(f, string(src), &direct); err != nil {
				t.Fatal(err)
			}
			if err := RunProgram(prog, &loaded); err != nil {
				t.Fatal(err)
			}
			if direct.String() != loaded.String() {
				t.Fatal("bytecode file produced different output")
			}
		})
	}
}

// TestExamples runs every example and compares with examples/expected.
// Run `go test ./pipeline -update` after changing an example on purpose.
func TestExamples(t *testing.T) {
	files, _ := filepath.Glob("../examples/*.sern")
	if len(files) == 0 {
		t.Fatal("no examples found")
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			src, _ := os.ReadFile(f)
			var out bytes.Buffer
			if err := Run(f, string(src), &out); err != nil {
				t.Fatal(FormatError(f, string(src), err))
			}
			golden := filepath.Join("../examples/expected", strings.TrimSuffix(filepath.Base(f), ".sern")+".out")
			if *update {
				os.MkdirAll(filepath.Dir(golden), 0o755)
				os.WriteFile(golden, out.Bytes(), 0o644)
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("missing golden file (run go test ./pipeline -update): %v", err)
			}
			if out.String() != string(want) {
				t.Fatalf("output changed.\ngot:\n%s\nwant:\n%s", out.String(), want)
			}
		})
	}
}

// disasm compiles src and returns the bytecode listing.
func disasm(t *testing.T, src string) string {
	t.Helper()
	syms, _ := NewRuntime(&bytes.Buffer{})
	proto, err := Compile(src, syms, compiler.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	object.Disassemble(&b, proto, syms.Names)
	return b.String()
}

func TestConstantFolding(t *testing.T) {
	cases := []struct{ src, want, notWant string }{
		{"print(4 * 2)", "; 8", "MUL"},
		{`print("Lab Mem " + 2 ** 3)`, `; "Lab Mem 8"`, "POW"},
		{"print(1 < 2 ? -3 : x)", "; -3", "JUMP"},
		{"print(false && x)", "FALSE", "GET_GLOBAL            41"},
		{"print(1 / 0)", "DIV", ""}, // left for run time, so the error keeps its traceback
	}
	for _, tc := range cases {
		out := disasm(t, "labmem x = 1\n"+tc.src)
		if !strings.Contains(out, tc.want) || (tc.notWant != "" && strings.Contains(out, tc.notWant)) {
			t.Errorf("%s: want %q and no %q in:\n%s", tc.src, tc.want, tc.notWant, out)
		}
	}
}

func TestSuperinstructions(t *testing.T) {
	out := disasm(t, "operation f(n) { n -= 1; kongroo n + 2 }")
	for _, want := range []string{"SUB_CONST", "ADD_CONST"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %s in:\n%s", want, out)
		}
	}
	got, err := run(t, `labmem s = "a"; s += 1; labmem f = 1.5; f -= 1; print(s, f, 3 - 5)`)
	if err != nil || got != "a1 0.5 -2\n" {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestTrace(t *testing.T) {
	var out, trace bytes.Buffer
	if err := RunWith("t.sern", "operation sq(n) { kongroo n * n }\nprint(sq(3))", &out, RunOptions{Trace: &trace}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "9\n" {
		t.Fatalf("output %q", out.String())
	}
	for _, want := range []string{"[ <operation sq> | 3 | 3 | 3 ]", "sq        0006    1  MUL"} {
		if !strings.Contains(trace.String(), want) {
			t.Errorf("trace lacks %q:\n%s", want, trace.String())
		}
	}
}

func TestUnusedWarnings(t *testing.T) {
	src := `operation f(a, b) {
    labmem unused = 1
    labmem used = 2
    labmem _ignored = 3
    labmem captured = 4
    operation helper() { kongroo 1 }
    reading i in range(3) { print(used) }
    {
        labmem inner = 5
    }
    kongroo operation() { kongroo captured }
}
{ labmem top = 1 }
labmem global = 1`
	var w bytes.Buffer
	syms, _ := NewRuntime(&bytes.Buffer{})
	if _, err := Compile(src, syms, compiler.Options{Warn: WarnTo(&w, "w.sern")}); err != nil {
		t.Fatal(err)
	}
	want := `w.sern:2:5: warning: lab member 'unused' is declared but never read
w.sern:6:5: warning: operation 'helper' is declared but never used
w.sern:7:5: warning: lab member 'i' is declared but never read
w.sern:9:9: warning: lab member 'inner' is declared but never read
w.sern:13:3: warning: lab member 'top' is declared but never read
`
	if w.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", w.String(), want)
	}
}

func TestTailCalls(t *testing.T) {
	// A tail call reuses the frame, so the traceback loses 'outer' and
	// shows how many frames were elided instead.
	src := "operation inner() {\n    kongroo 1 / 0\n}\noperation outer() {\n    kongroo inner()\n}\nouter()\n"
	_, err := run(t, src)
	msg := FormatError("t.sern", src, err)
	want := "  test.sern:7 in <script>\n  (... 1 tail call elided ...)\n  test.sern:2 in inner\n"
	if !strings.Contains(msg, want) {
		t.Errorf("traceback:\n%s\nwant it to contain:\n%s", msg, want)
	}
	if !strings.Contains(disasm(t, src), "TAIL_CALL") {
		t.Error("no TAIL_CALL emitted")
	}
	// With tail calls off, the same recursion overflows the frame stack.
	deep := "operation sum(n, acc) { if n == 0 { kongroo acc } kongroo sum(n - 1, acc + n) }\nsum(100000, 0)"
	syms, m := NewRuntime(&bytes.Buffer{})
	proto, err := Compile(deep, syms, compiler.Options{NoTailCalls: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Run(proto); err == nil || !strings.Contains(err.Error(), "Reading Steiner overload") {
		t.Errorf("expected a stack overflow without tail calls, got %v", err)
	}
}

func TestConstCompilesToValue(t *testing.T) {
	out := disasm(t, "const N = 6 * 7\nprint(N)")
	if !strings.Contains(out, "; 42") || strings.Contains(out, "; N") {
		t.Errorf("const reference should compile to CONST 42:\n%s", out)
	}
}
