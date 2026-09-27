package object

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// Encode serializes a data value to bytes so it can be stored in ECHELON.
// Only data travels through a D-Mail: nil, bools, numbers, strings, lists
// and maps. Operations and native handles cannot be sent.
func Encode(v Value) ([]byte, error) {
	return encode(nil, v, 0)
}

func encode(b []byte, v Value, depth int) ([]byte, error) {
	if depth > 64 {
		return nil, errors.New("value nested too deeply to D-Mail (possible cycle)")
	}
	switch v.K {
	case KNil:
		return append(b, 0), nil
	case KBool:
		return append(b, 1, byte(v.I)), nil
	case KInt:
		b = append(b, 2)
		return binary.AppendVarint(b, v.I), nil
	case KFloat:
		b = append(b, 3)
		return binary.BigEndian.AppendUint64(b, math.Float64bits(v.F)), nil
	case KString:
		s := v.AsString()
		b = append(b, 4)
		b = binary.AppendUvarint(b, uint64(len(s)))
		return append(b, s...), nil
	case KList:
		items := v.AsList().Items
		b = append(b, 5)
		b = binary.AppendUvarint(b, uint64(len(items)))
		var err error
		for _, it := range items {
			if b, err = encode(b, it, depth+1); err != nil {
				return nil, err
			}
		}
		return b, nil
	case KMap:
		m := v.AsMap()
		b = append(b, 6)
		b = binary.AppendUvarint(b, uint64(m.Len()))
		var err error
		for i := range m.keys {
			if b, err = encode(b, m.keys[i], depth+1); err != nil {
				return nil, err
			}
			if b, err = encode(b, m.vals[i], depth+1); err != nil {
				return nil, err
			}
		}
		return b, nil
	}
	return nil, fmt.Errorf("cannot D-Mail a %s through ECHELON (only data can be sent)", v.TypeName())
}

// Decode reverses Encode.
func Decode(b []byte) (Value, error) {
	v, rest, err := decode(b, 0)
	if err != nil {
		return Nil, err
	}
	if len(rest) != 0 {
		return Nil, errors.New("trailing bytes after encoded value")
	}
	return v, nil
}

var errShort = errors.New("truncated encoded value")

func decode(b []byte, depth int) (Value, []byte, error) {
	if depth > 64 {
		return Nil, nil, errors.New("encoded value nested too deeply")
	}
	if len(b) == 0 {
		return Nil, nil, errShort
	}
	tag, b := b[0], b[1:]
	switch tag {
	case 0:
		return Nil, b, nil
	case 1:
		if len(b) < 1 {
			return Nil, nil, errShort
		}
		return Bool(b[0] != 0), b[1:], nil
	case 2:
		i, n := binary.Varint(b)
		if n <= 0 {
			return Nil, nil, errShort
		}
		return Int(i), b[n:], nil
	case 3:
		if len(b) < 8 {
			return Nil, nil, errShort
		}
		return Float(math.Float64frombits(binary.BigEndian.Uint64(b))), b[8:], nil
	case 4:
		l, n := binary.Uvarint(b)
		if n <= 0 || uint64(len(b)-n) < l {
			return Nil, nil, errShort
		}
		return Str(string(b[n : n+int(l)])), b[n+int(l):], nil
	case 5:
		l, n := binary.Uvarint(b)
		if n <= 0 || l > uint64(len(b)) {
			return Nil, nil, errShort
		}
		b = b[n:]
		items := make([]Value, 0, l)
		for i := uint64(0); i < l; i++ {
			var it Value
			var err error
			if it, b, err = decode(b, depth+1); err != nil {
				return Nil, nil, err
			}
			items = append(items, it)
		}
		return ListOf(items), b, nil
	case 6:
		l, n := binary.Uvarint(b)
		if n <= 0 || l > uint64(len(b)) {
			return Nil, nil, errShort
		}
		b = b[n:]
		m := NewMap()
		for i := uint64(0); i < l; i++ {
			var k, v Value
			var err error
			if k, b, err = decode(b, depth+1); err != nil {
				return Nil, nil, err
			}
			if v, b, err = decode(b, depth+1); err != nil {
				return Nil, nil, err
			}
			if err := m.Set(k, v); err != nil {
				return Nil, nil, err
			}
		}
		return MapVal(m), b, nil
	}
	return Nil, nil, fmt.Errorf("unknown value tag %d", tag)
}
