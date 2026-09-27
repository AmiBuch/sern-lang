package vm

import (
	"fmt"
	"strings"
)

// TraceEntry is one frame of a Reading Steiner traceback.
type TraceEntry struct {
	Function string
	Line     int
	// TailCalls counts the frames that tail calls replaced on the way to
	// this one. Their functions and lines are gone; only the count remains.
	TailCalls int
}

// RuntimeError is an error raised while executing bytecode. It carries the
// call stack at the moment of failure.
type RuntimeError struct {
	Msg   string
	File  string
	Trace []TraceEntry // outermost first
}

func (e *RuntimeError) Error() string { return e.Msg }

// Report formats the error the way ibn-5100 prints it.
func (e *RuntimeError) Report() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Worldline divergence: %s\n", e.Msg)
	b.WriteString("Reading Steiner traceback (most recent worldline last):\n")
	for _, t := range e.Trace {
		if t.TailCalls > 0 {
			fmt.Fprintf(&b, "  (... %d tail call%s elided ...)\n", t.TailCalls, plural(t.TailCalls))
		}
		fmt.Fprintf(&b, "  %s:%d in %s\n", e.File, t.Line, t.Function)
	}
	b.WriteString("El Psy Kongroo.")
	return b.String()
}

func (vm *VM) runtimeError(format string, args ...any) *RuntimeError {
	e := &RuntimeError{Msg: fmt.Sprintf(format, args...), File: vm.File}
	if e.File == "" {
		e.File = "<input>"
	}
	for _, f := range vm.frames {
		line := 0
		lines := f.cl.Proto.Chunk.Lines
		if ip := f.ip - 1; ip >= 0 && ip < len(lines) {
			line = lines[ip]
		}
		e.Trace = append(e.Trace, TraceEntry{Function: f.cl.Proto.Name, Line: line, TailCalls: f.tail})
	}
	return e
}
