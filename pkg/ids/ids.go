// Package ids generates UUIDv7 identifiers.
//
// UUIDv7 is used for every Noema record because it is globally unique and
// time-ordered, which keeps event indexes dense and makes ids sort by creation
// time without a second column.
package ids

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// ID is a 128-bit UUID in canonical textual form.
type ID string

// Nil is the zero ID.
const Nil ID = ""

var (
	mu       sync.Mutex
	lastMS   int64
	lastSeq  uint16
	randPool = rand.Reader
)

// New returns a new UUIDv7. Within the same millisecond ids are made strictly
// increasing by a 12-bit counter so ordering is stable even under bursts.
func New() ID {
	var b [16]byte
	if _, err := randPool.Read(b[6:]); err != nil {
		panic("ids: crypto/rand unavailable: " + err.Error()) // no safe fallback exists
	}
	ms := time.Now().UnixMilli()
	mu.Lock()
	if ms <= lastMS {
		ms = lastMS
		lastSeq++
		if lastSeq >= 1<<12 { // counter overflow: borrow a millisecond
			lastMS++
			ms = lastMS
			lastSeq = 0
		}
	} else {
		lastMS = ms
		lastSeq = uint16(b[6])<<4 | uint16(b[7])>>4 // random start, per RFC 9562 method 1
		lastSeq &= 0x7ff                            // leave headroom for increments
	}
	seq := lastSeq
	mu.Unlock()

	b[0] = byte(ms >> 40) // #nosec G115 -- deliberate byte packing of a 48-bit millisecond timestamp
	b[1] = byte(ms >> 32) // #nosec G115 -- deliberate byte packing (UUIDv7 layout)
	b[2] = byte(ms >> 24) // #nosec G115 -- deliberate byte packing (UUIDv7 layout)
	b[3] = byte(ms >> 16) // #nosec G115 -- deliberate byte packing (UUIDv7 layout)
	b[4] = byte(ms >> 8)  // #nosec G115 -- deliberate byte packing (UUIDv7 layout)
	b[5] = byte(ms)       // #nosec G115 -- deliberate byte packing (UUIDv7 layout)
	// Version 7 in the high nibble of byte 6, then the 12-bit counter.
	binary.BigEndian.PutUint16(b[6:8], 0x7000|(seq&0x0fff))
	b[8] = 0x80 | (b[8] & 0x3f) // RFC 4122 variant
	return ID(format(b))
}

func format(b [16]byte) string {
	var out [36]byte
	hex.Encode(out[0:8], b[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], b[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], b[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], b[8:10])
	out[23] = '-'
	hex.Encode(out[24:36], b[10:16])
	return string(out[:])
}

// Parse validates the canonical textual form and returns it lower-cased.
func Parse(s string) (ID, error) {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return Nil, errors.New("ids: invalid uuid format")
	}
	var b [16]byte
	src := s[0:8] + s[9:13] + s[14:18] + s[19:23] + s[24:36]
	if _, err := hex.Decode(b[:], []byte(src)); err != nil {
		return Nil, errors.New("ids: invalid uuid hex")
	}
	return ID(format(b)), nil
}

// Time extracts the millisecond timestamp embedded in a UUIDv7. It returns the
// zero time for ids that are not version 7.
func (id ID) Time() time.Time {
	if len(id) != 36 {
		return time.Time{}
	}
	var b [16]byte
	src := string(id[0:8] + id[9:13] + id[14:18] + id[19:23] + id[24:36])
	if _, err := hex.Decode(b[:], []byte(src)); err != nil || b[6]>>4 != 7 {
		return time.Time{}
	}
	ms := int64(b[0])<<40 | int64(b[1])<<32 | int64(b[2])<<24 | int64(b[3])<<16 | int64(b[4])<<8 | int64(b[5])
	return time.UnixMilli(ms)
}

// String implements fmt.Stringer.
func (id ID) String() string { return string(id) }

// IsZero reports whether the id is unset.
func (id ID) IsZero() bool { return id == Nil }
