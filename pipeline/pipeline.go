// Package pipeline wires the stages together:
//
//	source ──lexer──▶ tokens ──parser──▶ AST ──compiler──▶ bytecode ──vm──▶ result
//
// It is what the ibn-5100 command and the tests use.
package pipeline

import (
	"fmt"
	"io"
	"strings"

	"sern/compiler"
	"sern/object"
	"sern/parser"
	"sern/stdlib"
	"sern/token"
	"sern/vm"
)

// NewRuntime creates a symbol table and a VM with the stdlib installed.
func NewRuntime(out io.Writer) (*object.Symbols, *vm.VM) {
	syms := object.NewSymbols()
	m := vm.New(syms, out)
	stdlib.Install(m)
	return syms, m
}

// Compile parses and compiles src against syms.
func Compile(src string, syms *object.Symbols, opts compiler.Options) (*object.FunctionProto, error) {
	prog, err := parser.Parse(src)
	if err != nil {
		return nil, err
	}
	return compiler.New(syms, opts).Compile(prog)
}

// RunOptions are the optional extras of RunWith.
type RunOptions struct {
	// Trace receives an instruction-by-instruction execution trace.
	Trace io.Writer
	// Warnings receives compiler warnings, one per line.
	Warnings io.Writer
}

// Run compiles and executes a source file, writing program output to out.
func Run(file, src string, out io.Writer) error { return RunWith(file, src, out, RunOptions{}) }

// RunWith is Run with tracing and warnings.
func RunWith(file, src string, out io.Writer, ro RunOptions) error {
	syms, m := NewRuntime(out)
	m.Trace = ro.Trace
	proto, err := Compile(src, syms, compiler.Options{Warn: WarnTo(ro.Warnings, file)})
	if err != nil {
		return err
	}
	m.File = file
	_, err = m.Run(proto)
	return err
}

// WarnTo returns a compiler warning callback that prints "file:line:col:
// warning: msg" lines to w, or nil (no warnings) if w is nil.
func WarnTo(w io.Writer, file string) func(token.Pos, string) {
	if w == nil {
		return nil
	}
	return func(pos token.Pos, msg string) {
		fmt.Fprintf(w, "%s:%d:%d: warning: %s\n", file, pos.Line, pos.Col, msg)
	}
}

// RunProgram executes a loaded .sernbc program.
func RunProgram(p *object.Program, out io.Writer) error { return RunProgramTraced(p, out, nil) }

// RunProgramTraced is RunProgram with an execution trace.
func RunProgramTraced(p *object.Program, out, trace io.Writer) error {
	syms := object.NewSymbols()
	for _, g := range p.Globals {
		syms.Slot(g)
	}
	m := vm.New(syms, out)
	stdlib.Install(m)
	m.Trace = trace
	m.File = p.Source
	_, err := m.Run(p.Script)
	return err
}

// FormatError renders any pipeline error for humans. Compile errors get
// the offending source line and a caret; runtime errors get a traceback.
func FormatError(file, src string, err error) string {
	var pos token.Pos
	var msg string
	switch e := err.(type) {
	case parser.ErrorList:
		msgs := make([]string, len(e))
		for i, pe := range e {
			msgs[i] = FormatError(file, src, pe)
		}
		return strings.Join(msgs, "\n")
	case *parser.Error:
		pos, msg = e.Pos, e.Msg
	case *compiler.Error:
		pos, msg = e.Pos, e.Msg
	case *vm.RuntimeError:
		return e.Report()
	default:
		return err.Error()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "IBN 5100 cannot decode %s:%d:%d: %s\n", file, pos.Line, pos.Col, msg)
	lines := strings.Split(src, "\n")
	if pos.Line >= 1 && pos.Line <= len(lines) {
		line := strings.ReplaceAll(lines[pos.Line-1], "\t", "    ")
		gutter := fmt.Sprintf("%5d | ", pos.Line)
		b.WriteString(gutter + line + "\n")
		// Columns count characters (runes), so index the line as runes.
		col := pos.Col
		if raw := []rune(lines[pos.Line-1]); col-1 <= len(raw) {
			col += 3 * strings.Count(string(raw[:max(0, col-1)]), "\t")
		}
		b.WriteString(strings.Repeat(" ", len(gutter)-2) + "| " + strings.Repeat(" ", max(0, col-1)) + "^")
	}
	return b.String()
}
