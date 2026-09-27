package object

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// The .sernbc ("Sern bytecode") file format written by `ibn-5100 build`:
//
//	magic     "IBN5100\x00"
//	version   uvarint
//	source    string            (file the program was compiled from)
//	globals   uvarint n, n × string
//	script    proto
//
//	proto     name string, arity uvarint,
//	          upvalues uvarint n, n × (isLocal byte, index uvarint),
//	          code bytes, lines uvarint n × uvarint (run-length: count, line),
//	          consts uvarint n, n × const
//	const     tag byte then payload: 0 nil | 1 bool byte | 2 int varint |
//	          3 float u64 bits | 4 string | 5 proto
//	string    uvarint length + bytes
const (
	sernbcMagic = "IBN5100\x00"
	// sernbcVersion 2 added POW, ADD_CONST, SUB_CONST and TAIL_CALL. Version 1 files use
	// a subset of version 2's opcodes, so they still load.
	sernbcVersion = 2
)

// Program is a loaded .sernbc file.
type Program struct {
	Source  string
	Globals []string
	Script  *FunctionProto
}

// WriteProgram serializes a compiled program.
func WriteProgram(w io.Writer, p *Program) error {
	var b bytes.Buffer
	b.WriteString(sernbcMagic)
	putUvarint(&b, sernbcVersion)
	putString(&b, p.Source)
	putUvarint(&b, uint64(len(p.Globals)))
	for _, g := range p.Globals {
		putString(&b, g)
	}
	if err := writeProto(&b, p.Script); err != nil {
		return err
	}
	_, err := w.Write(b.Bytes())
	return err
}

func writeProto(b *bytes.Buffer, p *FunctionProto) error {
	putString(b, p.Name)
	putUvarint(b, uint64(p.Arity))
	putUvarint(b, uint64(len(p.Upvalues)))
	for _, u := range p.Upvalues {
		if u.IsLocal {
			b.WriteByte(1)
		} else {
			b.WriteByte(0)
		}
		putUvarint(b, uint64(u.Index))
	}
	putUvarint(b, uint64(len(p.Chunk.Code)))
	b.Write(p.Chunk.Code)
	// run-length encode line numbers
	lines := p.Chunk.Lines
	var runs [][2]int
	for i := 0; i < len(lines); {
		j := i
		for j < len(lines) && lines[j] == lines[i] {
			j++
		}
		runs = append(runs, [2]int{j - i, lines[i]})
		i = j
	}
	putUvarint(b, uint64(len(runs)))
	for _, r := range runs {
		putUvarint(b, uint64(r[0]))
		putUvarint(b, uint64(r[1]))
	}
	putUvarint(b, uint64(len(p.Chunk.Consts)))
	for _, k := range p.Chunk.Consts {
		switch k.K {
		case KNil:
			b.WriteByte(0)
		case KBool:
			b.WriteByte(1)
			b.WriteByte(byte(k.I))
		case KInt:
			b.WriteByte(2)
			var tmp [binary.MaxVarintLen64]byte
			b.Write(tmp[:binary.PutVarint(tmp[:], k.I)])
		case KFloat:
			b.WriteByte(3)
			var tmp [8]byte
			binary.BigEndian.PutUint64(tmp[:], math.Float64bits(k.F))
			b.Write(tmp[:])
		case KString:
			b.WriteByte(4)
			putString(b, k.AsString())
		default:
			fp, ok := k.O.(*FunctionProto)
			if !ok {
				return fmt.Errorf("cannot serialize constant of type %s", k.TypeName())
			}
			b.WriteByte(5)
			if err := writeProto(b, fp); err != nil {
				return err
			}
		}
	}
	return nil
}

// ReadProgram deserializes a .sernbc file.
func ReadProgram(r io.Reader) (*Program, error) {
	br := bufio.NewReader(r)
	magic := make([]byte, len(sernbcMagic))
	if _, err := io.ReadFull(br, magic); err != nil || string(magic) != sernbcMagic {
		return nil, errors.New("not a Sern bytecode file (bad magic; the IBN 5100 cannot decode it)")
	}
	rd := &reader{r: br}
	if v := rd.uvarint(); v < 1 || v > sernbcVersion {
		return nil, fmt.Errorf("unsupported .sernbc version %d (this IBN 5100 reads 1 to %d)", v, sernbcVersion)
	}
	p := &Program{Source: rd.string()}
	n := rd.uvarint()
	for i := uint64(0); i < n && rd.err == nil; i++ {
		p.Globals = append(p.Globals, rd.string())
	}
	p.Script = rd.proto(0)
	if rd.err != nil {
		return nil, fmt.Errorf("corrupt .sernbc file: %w", rd.err)
	}
	return p, nil
}

type reader struct {
	r   *bufio.Reader
	err error
}

func (rd *reader) uvarint() uint64 {
	if rd.err != nil {
		return 0
	}
	v, err := binary.ReadUvarint(rd.r)
	rd.err = err
	return v
}

func (rd *reader) varint() int64 {
	if rd.err != nil {
		return 0
	}
	v, err := binary.ReadVarint(rd.r)
	rd.err = err
	return v
}

func (rd *reader) byte() byte {
	if rd.err != nil {
		return 0
	}
	c, err := rd.r.ReadByte()
	rd.err = err
	return c
}

func (rd *reader) bytes(n uint64) []byte {
	if rd.err != nil {
		return nil
	}
	if n > 1<<30 {
		rd.err = errors.New("length too large")
		return nil
	}
	buf := make([]byte, n)
	_, rd.err = io.ReadFull(rd.r, buf)
	return buf
}

func (rd *reader) string() string { return string(rd.bytes(rd.uvarint())) }

func (rd *reader) proto(depth int) *FunctionProto {
	if depth > 256 {
		rd.err = errors.New("operations nested too deeply")
		return nil
	}
	p := &FunctionProto{Name: rd.string(), Arity: int(rd.uvarint()), Chunk: &Chunk{}}
	nu := rd.uvarint()
	for i := uint64(0); i < nu && rd.err == nil; i++ {
		isLocal := rd.byte() == 1
		p.Upvalues = append(p.Upvalues, UpvalueDesc{IsLocal: isLocal, Index: uint16(rd.uvarint())})
	}
	p.Chunk.Code = rd.bytes(rd.uvarint())
	nr := rd.uvarint()
	for i := uint64(0); i < nr && rd.err == nil; i++ {
		count, line := rd.uvarint(), int(rd.uvarint())
		for j := uint64(0); j < count && j < 1<<24; j++ {
			p.Chunk.Lines = append(p.Chunk.Lines, line)
		}
	}
	if rd.err == nil && len(p.Chunk.Lines) != len(p.Chunk.Code) {
		rd.err = errors.New("line table does not match code length")
	}
	nc := rd.uvarint()
	for i := uint64(0); i < nc && rd.err == nil; i++ {
		switch tag := rd.byte(); tag {
		case 0:
			p.Chunk.Consts = append(p.Chunk.Consts, Nil)
		case 1:
			p.Chunk.Consts = append(p.Chunk.Consts, Bool(rd.byte() == 1))
		case 2:
			p.Chunk.Consts = append(p.Chunk.Consts, Int(rd.varint()))
		case 3:
			bits := binary.BigEndian.Uint64(rd.bytes(8))
			p.Chunk.Consts = append(p.Chunk.Consts, Float(math.Float64frombits(bits)))
		case 4:
			p.Chunk.Consts = append(p.Chunk.Consts, Str(rd.string()))
		case 5:
			p.Chunk.Consts = append(p.Chunk.Consts, ProtoConst(rd.proto(depth+1)))
		default:
			if rd.err == nil {
				rd.err = fmt.Errorf("unknown constant tag %d", tag)
			}
		}
	}
	return p
}

// ProtoConst wraps a prototype as a constant-pool entry.
func ProtoConst(p *FunctionProto) Value { return Value{K: KProto, O: p} }

func putUvarint(b *bytes.Buffer, v uint64) {
	var tmp [binary.MaxVarintLen64]byte
	b.Write(tmp[:binary.PutUvarint(tmp[:], v)])
}

func putString(b *bytes.Buffer, s string) {
	putUvarint(b, uint64(len(s)))
	b.WriteString(s)
}
