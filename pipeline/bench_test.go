package pipeline

import (
	"io"
	"testing"

	"sern/compiler"
)

const (
	fibSrc  = "operation fib(n) { if n < 2 { kongroo n } kongroo fib(n - 1) + fib(n - 2) }\nfib(25)\n"
	loopSrc = "operation count(n) { labmem s = 0; timeleap n > 0 { s += 2; n -= 1 } kongroo s }\ncount(1000000)\n"
)

func benchRun(b *testing.B, src string, opts compiler.Options) {
	for i := 0; i < b.N; i++ {
		syms, m := NewRuntime(io.Discard)
		proto, err := Compile(src, syms, opts)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := m.Run(proto); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkFib measures raw VM call and arithmetic throughput.
//
//	go test ./pipeline -bench Fib -cpuprofile cpu.out && go tool pprof -top cpu.out
func BenchmarkFib(b *testing.B) { benchRun(b, fibSrc, compiler.Options{}) }

// The NoSuper variants disable ADD_CONST/SUB_CONST (superinstructions).
func BenchmarkFibNoSuper(b *testing.B) {
	benchRun(b, fibSrc, compiler.Options{NoSuperinstructions: true})
}
func BenchmarkLoop(b *testing.B) { benchRun(b, loopSrc, compiler.Options{}) }
func BenchmarkLoopNoSuper(b *testing.B) {
	benchRun(b, loopSrc, compiler.Options{NoSuperinstructions: true})
}
