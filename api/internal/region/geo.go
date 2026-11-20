package region

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"net/netip"
	"os"
	"sort"
	"strings"
)

// Table maps an address to its country: ranges sorted by their first address,
// read from an IP-to-country CSV ("first,last,CC" per line, IPv4 and IPv6,
// gzipped or not). DB-IP's free country file has that shape (CC BY 4.0).
// IPv4 and IPv6 are kept apart, each compact (about 20 MB for the whole file).
type Table struct {
	v4 []span4
	v6 []span6
}

type span4 struct {
	first, last uint32
	cc          [2]byte
}

type span6 struct {
	first, last [16]byte
	cc          [2]byte
}

// MinRanges is the fewest ranges a real file has. A smaller one is a broken
// download, and the API won't start on it.
const MinRanges = 100_000

// Open reads the table at path; a name ending in .gz is decompressed.
func Open(path string) (*Table, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		z, err := gzip.NewReader(f)
		if err != nil {
			return nil, fmt.Errorf("region: %s: %w", path, err)
		}
		defer z.Close()
		r = z
	}
	return Read(r)
}

// Read parses the CSV. A line that doesn't parse ends the read with its line
// number: a table is complete or it isn't used.
func Read(r io.Reader) (*Table, error) {
	t := &Table{}
	sc := bufio.NewScanner(r)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) != 3 {
			return nil, fmt.Errorf("region: line %d: want 3 fields", n)
		}
		first, err1 := netip.ParseAddr(strings.Trim(parts[0], `"`))
		last, err2 := netip.ParseAddr(strings.Trim(parts[1], `"`))
		cc := strings.ToUpper(strings.Trim(parts[2], `"`))
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("region: line %d: not an address", n)
		}
		first, last = first.Unmap(), last.Unmap()
		if first.Is4() != last.Is4() || last.Less(first) || len(cc) != 2 {
			return nil, fmt.Errorf("region: line %d: not a range", n)
		}
		code := [2]byte{cc[0], cc[1]}
		if first.Is4() {
			t.v4 = append(t.v4, span4{first: v4(first), last: v4(last), cc: code})
		} else {
			t.v6 = append(t.v6, span6{first: first.As16(), last: last.As16(), cc: code})
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	sort.Slice(t.v4, func(i, j int) bool { return t.v4[i].first < t.v4[j].first })
	sort.Slice(t.v6, func(i, j int) bool { return bytes.Compare(t.v6[i].first[:], t.v6[j].first[:]) < 0 })
	return t, nil
}

// Len is the number of ranges.
func (t *Table) Len() int { return len(t.v4) + len(t.v6) }

// Country is the country of addr, or "" when no range holds it ("ZZ", the
// file's unknown, is returned as "" too).
func (t *Table) Country(addr netip.Addr) string {
	addr = addr.Unmap()
	var cc [2]byte
	switch {
	case addr.Is4():
		a := v4(addr)
		i := sort.Search(len(t.v4), func(i int) bool { return a < t.v4[i].first }) - 1
		if i < 0 || t.v4[i].last < a {
			return ""
		}
		cc = t.v4[i].cc
	case addr.Is6():
		a := addr.As16()
		i := sort.Search(len(t.v6), func(i int) bool { return bytes.Compare(a[:], t.v6[i].first[:]) < 0 }) - 1
		if i < 0 || bytes.Compare(t.v6[i].last[:], a[:]) < 0 {
			return ""
		}
		cc = t.v6[i].cc
	default:
		return ""
	}
	if cc == [2]byte{'Z', 'Z'} {
		return ""
	}
	return string(cc[:])
}

func v4(a netip.Addr) uint32 {
	b := a.As4()
	return binary.BigEndian.Uint32(b[:])
}
