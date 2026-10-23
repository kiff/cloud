package anchor

import (
	"encoding/binary"
	"errors"
	"sort"
	"time"
)

// Record is the minimal anchorable shape used by the PoC. It captures
// the framework-side audit identity (RecordID, TraceID, EntityID, Kind,
// At) without depending on the framework's JSON serialization, so byte
// stability is local to the cloud and not coupled to a framework
// release.
//
// The production form replaces this with a direct read of
// the framework's audit.Record stream, plus per-stream variants for
// usage and token consumption.
type Record struct {
	// RecordID is the audit-record identifier. Used as the proof key
	// returned by kiff-verify.
	RecordID string

	// TraceID is the framework's trace identifier for the operation
	// that produced the record. Optional but anchored when present.
	TraceID string

	// EntityID is the entity the record refers to (e.g. order id).
	EntityID string

	// Kind is the audit-record kind (action_executed, approval_granted,
	// event_ingested, etc.).
	Kind string

	// At is the wall-clock time the record was produced. Anchored at
	// nanosecond precision so two records in the same trace cannot
	// collide.
	At time.Time

	// Payload is opaque to the anchoring layer; whatever the caller
	// considers material to the record's meaning. The PoC hashes the
	// raw bytes; production may switch to a hash of a canonical
	// framework-side encoding.
	Payload []byte
}

// CanonicalEncoding returns the deterministic byte representation of
// a record, used as the input to LeafHash. The format is intentionally
// simple: each field length-prefixed with a 4-byte big-endian length,
// followed by the field bytes; At as a 8-byte big-endian unix-nano.
//
// Stable across the PoC. If we ever change it, every previously
// anchored root becomes uninterpretable, so changes go through an RFC.
func (r Record) CanonicalEncoding() []byte {
	var out []byte
	out = appendLP(out, []byte(r.RecordID))
	out = appendLP(out, []byte(r.TraceID))
	out = appendLP(out, []byte(r.EntityID))
	out = appendLP(out, []byte(r.Kind))

	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], uint64(r.At.UnixNano()))
	out = append(out, ts[:]...)

	out = appendLP(out, r.Payload)
	return out
}

// appendLP writes a length-prefixed byte string.
func appendLP(dst, src []byte) []byte {
	var lp [4]byte
	binary.BigEndian.PutUint32(lp[:], uint32(len(src)))
	dst = append(dst, lp[:]...)
	dst = append(dst, src...)
	return dst
}

// Batch is an ordered set of records committed to a single Merkle root.
// Records keep their submission order; ordering is preserved so that
// proof indices are stable.
//
// Salt is the per-tenant secret mixed into every leaf hash in this
// batch. Empty for the pre-#107 unsalted form; for salted
// batches the cloud reads it from the issuing tenant's
// cloud_meta row before constructing the batch. Off-chain
// verification needs the same salt to recompute the leaf, so
// kiff-verify accepts it via --salt or directly inside the batch
// JSON envelope.
type Batch struct {
	BatchID  uint64
	StreamID Hash
	Salt     []byte `json:",omitempty"`
	Records  []Record
}

// LeafHashes returns the keccak-256 leaf hashes for the batch's
// records, in batch order. For salted batches the salt is
// prepended to every leaf preimage; the unsalted code path is
// preserved when len(b.Salt) == 0.
func (b *Batch) LeafHashes() []Hash {
	out := make([]Hash, len(b.Records))
	for i, r := range b.Records {
		out[i] = LeafHash(b.Salt, r.CanonicalEncoding())
	}
	return out
}

// IndexOf returns the position of a record id in the batch, or -1 if
// it is not present.
func (b *Batch) IndexOf(recordID string) int {
	for i, r := range b.Records {
		if r.RecordID == recordID {
			return i
		}
	}
	return -1
}

// Validate checks that the batch is structurally usable for anchoring:
// non-empty, no duplicate RecordIDs, every Record has a non-empty
// RecordID. Returns the first violation; intended for use at the
// boundary where records enter the anchoring path.
func (b *Batch) Validate() error {
	if len(b.Records) == 0 {
		return errors.New("anchor: batch has no records")
	}
	seen := make(map[string]struct{}, len(b.Records))
	for i, r := range b.Records {
		if r.RecordID == "" {
			return errors.New("anchor: batch contains a record with empty RecordID")
		}
		if _, dup := seen[r.RecordID]; dup {
			return errors.New("anchor: batch contains duplicate RecordID " + r.RecordID)
		}
		seen[r.RecordID] = struct{}{}
		_ = i
	}
	return nil
}

// SortByRecordID returns a copy of the batch sorted by RecordID. Used
// when callers want a canonical anchored order independent of
// submission order. Not used by the PoC's default path.
func (b *Batch) SortByRecordID() *Batch {
	cp := make([]Record, len(b.Records))
	copy(cp, b.Records)
	sort.Slice(cp, func(i, j int) bool { return cp[i].RecordID < cp[j].RecordID })
	return &Batch{BatchID: b.BatchID, StreamID: b.StreamID, Records: cp}
}

// StreamID returns the canonical stream identifier for the named
// stream. v0.1 (PoC) defines only "audit"; "usage" and "token" land
// with the production form.
func StreamID(name string) Hash {
	return keccak256([]byte(name))
}
