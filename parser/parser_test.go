package parser

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"sern/ast"
)

// newlineCases exercise every place where a line break is (or isn't)
// significant.
var newlineCases = []string{
	"labmem x = 1\nlabmem y = 2",
	"labmem x =\n  1 +\n  2",
	"labmem x = 1\n  + 2", // an operator on the next line continues the expression
	"f\n(1)",              // ...but '(' and '[' start a new statement
	"f\n[1]",
	"x\n.y\n.sern()",
	"operation f() {\n  kongroo\n}\noperation g() {\n  kongroo\n  1\n}",
	"if a {\n} \n else if b {\n}\nelse {\n}",
	"labmem m = {\n  a: 1,\n  b\n  : 2\n}\nlabmem l = [\n  1,\n  2\n]",
	"x\n= 5\ny\n+= 1",
	"x = 1\n;\nprint(x)",
	"reading k,\n v in m {\n}",
	"operation\nf(a,\n b) {\n}",
	"print(\"a {x}\"\n)",
	"match x {\n  1 => a()\n  2 => {\n    b()\n  }, 3 => kongroo\n  _ => c(),\n}",
	"const N = 1 +\n  2",
	"x = a\n  ? b\n  : c",
	"x = 2\n  ** 3",
}

func TestNewlineDesignsAgree(t *testing.T) {
	srcs := map[string]string{}
	for i, c := range newlineCases {
		srcs[string(rune('a'+i))] = c
	}
	files, _ := filepath.Glob("../examples/*.sern")
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		srcs[f] = string(b)
	}
	for name, src := range srcs {
		a, errA := Parse(src)
		b, errB := ParseNewlineTokens(src)
		if (errA == nil) != (errB == nil) || (errA != nil && errA.Error() != errB.Error()) {
			t.Errorf("%s: errors differ: flag=%v tokens=%v", name, errA, errB)
			continue
		}
		if !reflect.DeepEqual(a, b) {
			t.Errorf("%s: trees differ\nflag:\n%s\ntokens:\n%s", name, ast.Dump(a), ast.Dump(b))
		}
	}
}

func TestErrorRecovery(t *testing.T) {
	src := `labmem a = 1 +
labmem = 2
operation f() {
    print(1 2)
    labmem ok = 3
    kongroo )
}
labmem b = [1, 2
print(a)`
	_, err := Parse(src)
	errs, ok := err.(ErrorList)
	if !ok {
		t.Fatalf("got %T %v", err, err)
	}
	// Line 2's own mistake goes unreported: the error from line 1 consumed
	// 'labmem' and recovery skipped the rest of that line.
	want := []string{
		"2:1: expected an expression, found keyword 'labmem'",
		"4:13: expected ')' to close call arguments, found number 2",
		"6:13: expected an expression, found ')'",
		"9:1: expected ']' to close list, found identifier 'print'",
	}
	var got []string
	for _, e := range errs {
		got = append(got, e.Error())
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

var precedenceCases = []string{
	"print(1 + 2 * 3 - 4 / 5 % 6, -x ** 2, 2 ** 3 ** 2, 2 ** -1, !a == b)",
	"x = a || b && c == d < e + f * g ** h",
	"x = a ? b : c ? d : e",
	"x = a || b ? c && d : e",
	"x = f(1)(2)[3].y.sern(4) ** -g.h[i]",
	"x = [1, {k: a ? 1 : 2}, (1 + 2) * 3]",
	"x = a\n  + b\n  .c\nf\n(1)",
	"x = \"s {a ** 2 ? b : c}\"",
	"x = -!-a",
}

func TestTowerAgrees(t *testing.T) {
	srcs := append([]string{}, precedenceCases...)
	srcs = append(srcs, newlineCases...)
	files, _ := filepath.Glob("../examples/*.sern")
	for _, f := range files {
		b, _ := os.ReadFile(f)
		srcs = append(srcs, string(b))
	}
	for _, src := range srcs {
		a, errA := Parse(src)
		b, errB := ParseTower(src)
		if (errA == nil) != (errB == nil) || (errA != nil && errA.Error() != errB.Error()) {
			t.Errorf("%q: errors differ: pratt=%v tower=%v", src, errA, errB)
			continue
		}
		if !reflect.DeepEqual(a, b) {
			t.Errorf("%q: trees differ\npratt:\n%s\ntower:\n%s", src, ast.Dump(a), ast.Dump(b))
		}
	}
}

func benchmarkParse(b *testing.B, parse func(string) (*ast.Program, error)) {
	files, _ := filepath.Glob("../examples/*.sern")
	var srcs []string
	for _, f := range files {
		src, _ := os.ReadFile(f)
		srcs = append(srcs, string(src))
	}
	b.ResetTimer()
	for range b.N {
		for _, src := range srcs {
			if _, err := parse(src); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkPratt(b *testing.B) { benchmarkParse(b, Parse) }
func BenchmarkTower(b *testing.B) { benchmarkParse(b, ParseTower) }

func TestRecoverySyncsAtLineStart(t *testing.T) {
	// The first error consumes the '}' that starts line 2; recovery must
	// still stop at 'match' on line 3 instead of skipping it and reporting
	// a bogus error at its closing brace.
	_, err := Parse("labmem a = [1,\n}\nmatch 5 { 1 => print(1) }")
	if got := err.Error(); got != "2:1: expected an expression, found '}'" {
		t.Errorf("got %q", got)
	}
}
