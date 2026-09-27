# Sern · the IBN 5100 toolchain

> *"The IBN 5100 can decode SERN's legacy language."*

**Sern** is a small programming language with a real compiler, a bytecode
virtual machine, and a Dynamo-style distributed database, **ECHELON**, built
in. The toolchain is called **`ibn-5100`**, after the one machine in
Steins;Gate that can read SERN's old code.

It's a learning project. Every component is written from scratch in Go,
with no dependencies beyond the standard library.

```sern
// Two clients on opposite sides of a network partition edit the same cart.
labmem db = echelon.cluster({nodes: 3, n: 3, r: 1, w: 1})
db.put("cart", ["Dr Pepper"])
labmem ctx = db.get("cart").context

db.partition(["node-1"], ["node-2", "node-3"])
db.via("node-1").put("cart", ["Dr Pepper", "Banana"], ctx)
db.via("node-2").put("cart", ["Dr Pepper", "Upa plushie"], ctx)
db.heal()
db.antientropy()

labmem r = db.get("cart")
print(r.worldlines)   // [["Dr Pepper", "Banana"], ["Dr Pepper", "Upa plushie"]]: divergent worldlines
```

## Quick start

Requires Go 1.24+.

```sh
git clone https://github.com/AmiBuch/sern-lang.git
cd sern-lang
make                      # builds bin/ibn-5100 and runs the tests
./bin/ibn-5100 run examples/hello.sern
./bin/ibn-5100 repl
make install              # symlinks ibn-5100 into ~/.local/bin (override with PREFIX=...)
make examples             # runs every program in examples/
```

Without `make`:

```sh
go build -o bin/ibn-5100 ./cmd/ibn-5100
go run ./cmd/ibn-5100 run examples/hello.sern
```

## The toolchain

```
ibn-5100 run [--trace] <file.sern|.sernbc>  compile (if needed) and run; --trace shows each instruction
ibn-5100 build <file.sern> [-o out.sernbc]  compile to a bytecode file
ibn-5100 exec <file.sernbc>                 run a bytecode file
ibn-5100 disasm <file.sern|file.sernbc>     bytecode listing
ibn-5100 tokens <file.sern>                 lexer output
ibn-5100 ast <file.sern>                    parser output
ibn-5100 repl                               interactive session
```

Every stage of the pipeline has its own subcommand, so you can inspect each
one:

```
source ─▶ lexer ─▶ tokens ─▶ parser ─▶ AST ─▶ compiler ─▶ bytecode ─▶ VM
          (tokens)           (ast)            (disasm / build)        (run / exec)
```

## The language in 20 lines

```sern
labmem lab = {name: "Future Gadget Lab", members: ["Okabe", "Mayuri", "Daru"]}

operation greet(who) {                       // functions are "operations"
    kongroo "Tuturu~ " + who                 // "kongroo" returns
}

reading m in lab.members { print(greet(m)) } // for-each ("Reading Steiner")

labmem n = 0
timeleap n < 3 { n += 1 }                    // while loop

operation counter() {                        // closures capture variables
    labmem hits = 0
    kongroo operation() { hits += 1; kongroo hits }
}
labmem c = counter(); c()
print(c(), map([1, 2, 3], operation(x) { kongroo x * x }))   // 2 [1, 4, 9]
```

| Keyword | Meaning |
|---|---|
| `labmem` | declare a variable |
| `operation` | declare a function, or a function literal |
| `kongroo` | return |
| `timeleap cond { ... }` | while loop |
| `reading x in xs { ... }` | for-each; `reading i, x in xs` also gives the index |
| `if` / `else`, `break` / `continue` | as usual |
| `true`, `false`, `nil` | literals |

Semicolons are optional. `//` starts a comment.

## ECHELON: Dynamo, simulated

A simulated cluster that implements the mechanisms of *Dynamo: Amazon's Highly
Available Key-value Store* (DeCandia et al., SOSP 2007):

| Mechanism | Paper | Code |
|---|---|---|
| Consistent hashing with virtual nodes, preference lists | §4.2–4.3 | `echelon/ring.go` |
| Vector clocks, syntactic reconciliation, siblings | §4.4 | `echelon/vclock.go`, `version.go` |
| Coordinated get/put with N, R, W quorums | §4.5 | `echelon/coordinator.go` |
| Sloppy quorum and hinted handoff | §4.6 | `coordinator.go`, `maintenance.go` |
| Merkle-tree anti-entropy per key range | §4.7 | `merkle.go`, `maintenance.go` |
| Gossip membership, join and leave | §4.8–4.9 | `membership.go`, `maintenance.go` |
| Read repair | §5 | `coordinator.go` |

Nodes interact only through a message `Transport`. The in-process
`SimNetwork` can crash nodes, wipe disks, partition the network and add
latency. It's deterministic by default, so every example prints the same
output on every run.

## Examples

| File | Shows |
|---|---|
| `hello.sern` | the first transmission |
| `lab_members.sern` | lists, maps, closures, higher-order operations |
| `fib.sern` | recursion vs. `timeleap` |
| `echelon_basics.sern` | ring, preference lists, put/get, contexts, deletes |
| `ring_balance.sern` | virtual-node balance; keys moved on join vs `hash % N` |
| `divergence.sern` | partition → concurrent writes → siblings → semantic merge |
| `hinted_handoff.sern` | sloppy quorum stand-ins and hint delivery |
| `anti_entropy.sern` | rebuilding a wiped node with Merkle trees |
| `gossip.sern` | a join spreading through the cluster |
| `experiments/staleness.sern` | stale-read rates for different N, R, W (non-deterministic) |

## Project layout

```
cmd/ibn-5100/   the CLI and REPL
token/ lexer/   characters → tokens
ast/ parser/    tokens → syntax tree (recursive descent + Pratt)
compiler/       syntax tree → bytecode (single pass, clox-style)
object/         values, opcodes, disassembler, .sernbc format, value codec
vm/             stack-based virtual machine
stdlib/         builtins and the `echelon` module
echelon/        the Dynamo implementation and network simulator
pipeline/       glue + end-to-end and golden tests
examples/       example programs; expected/ holds their golden output
experiments/    non-deterministic experiments
```

## Tests

```sh
go test ./...                    # unit, end-to-end and golden-output tests
go test -race ./...              # the cluster is concurrent: keep it race-free
go test ./pipeline -update       # re-record golden output after an intended change
go test ./pipeline -bench Fib    # VM benchmark
```

## Roadmap

- **Phase 2:** an LLVM backend (`ibn-5100 build --target=native`) sharing the
  same front end.
- **Phase 3:** ECHELON over TCP, with one process per node.

El Psy Kongroo.
