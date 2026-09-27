// Command ibn-5100 is the Sern toolchain: compiler, disassembler, VM and REPL.
//
// In Steins;Gate the IBN 5100 is the only machine that can read SERN's old
// proprietary language. Here it reads Sern.
package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"sern/ast"
	"sern/compiler"
	"sern/lexer"
	"sern/object"
	"sern/parser"
	"sern/pipeline"
	"sern/stdlib"
	"sern/token"
	"sern/vm"
)

const version = "0.1.0"

const banner = `IBN 5100 — Sern compiler & worldline VM v` + version + `
Divergence 1.048596. El Psy Kongroo.`

func usage() {
	fmt.Fprint(os.Stderr, banner+`

usage: ibn-5100 <command> [arguments]

commands:
  run [--trace] <file.sern|.sernbc>  compile (if needed) and run a program;
                                     --trace prints each instruction and the stack
  build <file.sern> [-o out.sernbc]  compile to a Sern bytecode file
  exec <file.sernbc>                 run a compiled bytecode file
  disasm <file.sern|file.sernbc>     show the bytecode listing
  tokens <file.sern>                 show the lexer's token stream
  ast <file.sern>                    show the parser's syntax tree
  repl                               interactive session
  version                            print version

`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "run":
		err = cmdRun(args)
	case "build":
		err = cmdBuild(args)
	case "exec":
		err = cmdRun(args)
	case "disasm":
		err = cmdDisasm(args)
	case "tokens":
		err = cmdTokens(args)
	case "ast":
		err = cmdAST(args)
	case "repl":
		err = repl(os.Stdin, os.Stdout)
	case "version", "--version", "-v":
		fmt.Println(banner)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "ibn-5100: unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		if err != errReported {
			fmt.Fprintln(os.Stderr, "ibn-5100:", err)
		}
		os.Exit(1)
	}
}

// errReported means the error was already printed.
var errReported = fmt.Errorf("reported")

func oneFile(args []string, what string) (string, error) {
	if len(args) != 1 {
		return "", fmt.Errorf("%s needs exactly one file", what)
	}
	return args[0], nil
}

func report(file, src string, err error) error {
	fmt.Fprintln(os.Stderr, pipeline.FormatError(file, src, err))
	return errReported
}

func isBytecode(path string) bool { return strings.HasSuffix(path, ".sernbc") }

func loadBytecode(path string) (*object.Program, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return object.ReadProgram(f)
}

// compileFile compiles a .sern source file into a Program.
func compileFile(path string) (*object.Program, string, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	syms, _ := pipeline.NewRuntime(io.Discard)
	proto, err := pipeline.Compile(string(src), syms, compiler.Options{Warn: pipeline.WarnTo(os.Stderr, path)})
	if err != nil {
		return nil, string(src), report(path, string(src), err)
	}
	return &object.Program{Source: path, Globals: syms.Names, Script: proto}, string(src), nil
}

func cmdRun(args []string) error {
	var files []string
	trace := false
	for _, a := range args {
		if a == "--trace" || a == "-trace" {
			trace = true
		} else {
			files = append(files, a)
		}
	}
	path, err := oneFile(files, "run")
	if err != nil {
		return err
	}
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	// The trace shares the program's writer so the two interleave in order.
	var tr io.Writer
	if trace {
		tr = out
	}
	if isBytecode(path) {
		p, err := loadBytecode(path)
		if err != nil {
			return err
		}
		if err := pipeline.RunProgramTraced(p, out, tr); err != nil {
			out.Flush()
			return report(path, "", err)
		}
		return nil
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := pipeline.RunWith(path, string(src), out, pipeline.RunOptions{Trace: tr, Warnings: os.Stderr}); err != nil {
		out.Flush()
		return report(path, string(src), err)
	}
	return nil
}

func cmdBuild(args []string) error {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	outPath := fs.String("o", "", "output file (default: <input>.sernbc)")
	// allow flags after the file name too
	var files []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return err
		}
		args = fs.Args()
		if len(args) > 0 {
			files = append(files, args[0])
			args = args[1:]
		}
	}
	path, err := oneFile(files, "build")
	if err != nil {
		return err
	}
	prog, _, err := compileFile(path)
	if err != nil {
		return err
	}
	if *outPath == "" {
		*outPath = strings.TrimSuffix(path, filepath.Ext(path)) + ".sernbc"
	}
	var buf bytes.Buffer
	if err := object.WriteProgram(&buf, prog); err != nil {
		return err
	}
	if err := os.WriteFile(*outPath, buf.Bytes(), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "IBN 5100: decoded %s → %s (%d bytes)\n", path, *outPath, buf.Len())
	return nil
}

func cmdDisasm(args []string) error {
	path, err := oneFile(args, "disasm")
	if err != nil {
		return err
	}
	var prog *object.Program
	if isBytecode(path) {
		if prog, err = loadBytecode(path); err != nil {
			return err
		}
	} else if prog, _, err = compileFile(path); err != nil {
		return err
	}
	object.Disassemble(os.Stdout, prog.Script, prog.Globals)
	return nil
}

func cmdTokens(args []string) error {
	path, err := oneFile(args, "tokens")
	if err != nil {
		return err
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	toks, err := lexer.Tokenize(string(src))
	for _, t := range toks {
		nl := " "
		if t.NewlineBefore {
			nl = "↵"
		}
		fmt.Printf("%4d:%-3d %s %s\n", t.Pos.Line, t.Pos.Col, nl, t)
	}
	if err != nil {
		le := err.(*lexer.Error)
		return report(path, string(src), &parser.Error{Pos: le.Pos, Msg: le.Msg})
	}
	return nil
}

func cmdAST(args []string) error {
	path, err := oneFile(args, "ast")
	if err != nil {
		return err
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	prog, err := parser.Parse(string(src))
	if err != nil {
		return report(path, string(src), err)
	}
	fmt.Print(ast.Dump(prog))
	return nil
}

// repl runs an interactive session. Globals persist between inputs; an
// input with unclosed braces continues on the next line.
func repl(in io.Reader, out io.Writer) error {
	fmt.Fprintln(out, banner)
	fmt.Fprintln(out, `Type Sern statements. Expressions print their value. ":help" for commands, ":q" to quit.`)
	syms, m := pipeline.NewRuntime(out)
	m.File = "<repl>"
	comp := compiler.New(syms, compiler.Options{Repl: true})
	sc := bufio.NewScanner(in)
	var pending strings.Builder
	prompt := "sern> "
	for {
		fmt.Fprint(out, prompt)
		if !sc.Scan() {
			fmt.Fprintln(out)
			return sc.Err()
		}
		line := sc.Text()
		if pending.Len() == 0 {
			switch strings.TrimSpace(line) {
			case ":q", ":quit", ":exit":
				fmt.Fprintln(out, "El Psy Kongroo.")
				return nil
			case ":help":
				fmt.Fprintln(out, "keywords:", strings.Join(token.Keywords(), " "))
				fmt.Fprintln(out, "builtins:", strings.Join(stdlib.Names(), " "))
				continue
			case "":
				continue
			}
		}
		pending.WriteString(line)
		pending.WriteString("\n")
		src := pending.String()
		prog, err := parser.Parse(src)
		if err != nil {
			if el, ok := err.(parser.ErrorList); ok && el[0].AtEOF {
				prompt = "  ..> "
				continue
			}
			fmt.Fprintln(out, pipeline.FormatError("<repl>", src, err))
			pending.Reset()
			prompt = "sern> "
			continue
		}
		pending.Reset()
		prompt = "sern> "
		proto, err := comp.Compile(prog)
		if err != nil {
			fmt.Fprintln(out, pipeline.FormatError("<repl>", src, err))
			continue
		}
		if _, err := m.Run(proto); err != nil {
			if re, ok := err.(*vm.RuntimeError); ok {
				fmt.Fprintln(out, re.Report())
			} else {
				fmt.Fprintln(out, err)
			}
		}
	}
}
