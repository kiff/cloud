package auth

import (
	"encoding/binary"
	"errors"
	"math"
)

// A CBOR reader for what a passkey's registration carries (RFC 8949): the
// attestationObject and the COSE key inside its authenticator data. Only the
// subset WebAuthn uses with attestation "none" is read, and everything else
// is refused rather than skipped:
//
//   - unsigned and negative integers (major types 0, 1), as int64;
//   - byte and text strings (2, 3), as []byte and string;
//   - arrays (4), as []any, and maps (5), as cborMap, whose keys are int64 or
//     string, each at most once;
//   - definite lengths only: an indefinite length, a tag (6), a float or a
//     simple value (7) is refused, and so is a reserved length (28 to 30);
//   - at most cborMaxDepth arrays and maps deep, and no length longer than
//     what's left of the input, so nothing is allocated past the input.

// ErrCBOR is CBOR KIFF doesn't read: malformed, cut short, or outside the
// subset above.
var ErrCBOR = errors.New("auth: the CBOR isn't the subset a passkey registration uses")

const cborMaxDepth = 4

// cborMap is a CBOR map with its keys as read: int64 or string.
type cborMap map[any]any

// decodeCBOR reads one CBOR item from the start of b, and returns it with
// how many bytes it took.
func decodeCBOR(b []byte) (any, int, error) {
	r := cborReader{b: b}
	v, err := r.item(0)
	if err != nil {
		return nil, 0, err
	}
	return v, r.at, nil
}

// decodeCBORWhole reads exactly one CBOR item that is all of b.
func decodeCBORWhole(b []byte) (any, error) {
	v, n, err := decodeCBOR(b)
	if err != nil {
		return nil, err
	}
	if n != len(b) {
		return nil, ErrCBOR
	}
	return v, nil
}

type cborReader struct {
	b  []byte
	at int
}

func (r *cborReader) left() int { return len(r.b) - r.at }

// head reads an item's major type and its argument.
func (r *cborReader) head() (major byte, arg uint64, err error) {
	if r.left() < 1 {
		return 0, 0, ErrCBOR
	}
	first := r.b[r.at]
	r.at++
	major, info := first>>5, first&0x1f
	switch {
	case info < 24:
		return major, uint64(info), nil
	case info == 24, info == 25, info == 26, info == 27:
		n := 1 << (info - 24)
		if r.left() < n {
			return 0, 0, ErrCBOR
		}
		p := r.b[r.at : r.at+n]
		r.at += n
		switch n {
		case 1:
			arg = uint64(p[0])
		case 2:
			arg = uint64(binary.BigEndian.Uint16(p))
		case 4:
			arg = uint64(binary.BigEndian.Uint32(p))
		default:
			arg = binary.BigEndian.Uint64(p)
		}
		return major, arg, nil
	default:
		// 28 to 30 are reserved, 31 is an indefinite length.
		return 0, 0, ErrCBOR
	}
}

func (r *cborReader) item(depth int) (any, error) {
	major, arg, err := r.head()
	if err != nil {
		return nil, err
	}
	switch major {
	case 0:
		if arg > math.MaxInt64 {
			return nil, ErrCBOR
		}
		return int64(arg), nil
	case 1:
		if arg > math.MaxInt64 {
			return nil, ErrCBOR
		}
		return -1 - int64(arg), nil
	case 2, 3:
		if arg > uint64(r.left()) {
			return nil, ErrCBOR
		}
		s := r.b[r.at : r.at+int(arg)]
		r.at += int(arg)
		if major == 3 {
			return string(s), nil
		}
		return append([]byte(nil), s...), nil
	case 4, 5:
		// Each item is at least one byte: a count past what's left can't be.
		if depth >= cborMaxDepth || arg > uint64(r.left()) {
			return nil, ErrCBOR
		}
		if major == 4 {
			out := make([]any, 0, arg)
			for i := uint64(0); i < arg; i++ {
				v, err := r.item(depth + 1)
				if err != nil {
					return nil, err
				}
				out = append(out, v)
			}
			return out, nil
		}
		out := make(cborMap, arg)
		for i := uint64(0); i < arg; i++ {
			k, err := r.item(depth + 1)
			if err != nil {
				return nil, err
			}
			switch k.(type) {
			case int64, string:
			default:
				return nil, ErrCBOR
			}
			if _, dup := out[k]; dup {
				return nil, ErrCBOR
			}
			v, err := r.item(depth + 1)
			if err != nil {
				return nil, err
			}
			out[k] = v
		}
		return out, nil
	default:
		// Tags (6), floats and simple values (7).
		return nil, ErrCBOR
	}
}
