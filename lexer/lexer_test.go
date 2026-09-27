package lexer

import (
	"strings"
	"testing"
)

func TestInterpolationTokens(t *testing.T) {
	cases := []struct{ src, want string }{
		{`"plain {} text"`, `STRING "plain {} text" | EOF`},
		{`"a{x}b{y}c"`, `STRING_PART "a" | IDENT x | STRING_PART "b" | IDENT y | STRING_END "c" | EOF`},
		{`"{ {k: 1}.k }"`, `STRING_PART "" | { | IDENT k | : | INT 1 | } | . | IDENT k | STRING_END "" | EOF`},
		{`"a {"b {x}"} c"`, `STRING_PART "a " | STRING_PART "b " | IDENT x | STRING_END "" | STRING_END " c" | EOF`},
		{`0xFF 0b10`, `INT 0xFF | INT 0b10 | EOF`},
	}
	for _, tc := range cases {
		toks, err := Tokenize(tc.src)
		if err != nil {
			t.Fatalf("%s: %v", tc.src, err)
		}
		var got []string
		for _, tok := range toks {
			got = append(got, strings.Join(strings.Fields(tok.String()), " "))
		}
		if g := strings.Join(got, " | "); g != tc.want {
			t.Errorf("%s:\n got  %s\n want %s", tc.src, g, tc.want)
		}
	}
}

func TestUnicodeIdentifiers(t *testing.T) {
	toks, err := Tokenize("labmem 岡部 = \"é\" + ñ2")
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		lit string
		col int
	}{{"labmem", 1}, {"岡部", 8}, {"=", 11}, {"é", 13}, {"+", 17}, {"ñ2", 19}}
	for i, w := range want {
		if toks[i].Lit != w.lit && toks[i].Type.String() != w.lit {
			t.Errorf("token %d: got %q, want %q", i, toks[i].Lit, w.lit)
		}
		if toks[i].Pos.Col != w.col {
			t.Errorf("token %d (%s): col %d, want %d (columns count runes)", i, w.lit, toks[i].Pos.Col, w.col)
		}
	}
	if _, err := Tokenize("x = 1 → 2"); err == nil || !strings.Contains(err.Error(), "1:7: unexpected character '→'") {
		t.Errorf("got %v", err)
	}
}
