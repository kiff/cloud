package anchor

import (
	"bytes"
	"testing"
	"time"
)

func sampleRecord(id string) Record {
	return Record{
		RecordID: id,
		TraceID:  "trace-" + id,
		EntityID: "order-" + id,
		Kind:     "action_executed",
		At:       time.Unix(1717000000, 0).UTC(),
		Payload:  []byte("payload-" + id),
	}
}

func TestCanonicalEncodingIsDeterministic(t *testing.T) {
	t.Parallel()

	r := sampleRecord("abc")
	a, b := r.CanonicalEncoding(), r.CanonicalEncoding()
	if !bytes.Equal(a, b) {
		t.Fatalf("canonical encoding differs across calls")
	}
}

func TestCanonicalEncodingChangesWithFields(t *testing.T) {
	t.Parallel()

	r1 := sampleRecord("abc")
	r2 := r1
	r2.RecordID = "abd"
	if bytes.Equal(r1.CanonicalEncoding(), r2.CanonicalEncoding()) {
		t.Fatal("changing RecordID did not change canonical encoding")
	}
}

func TestBatchValidate(t *testing.T) {
	t.Parallel()

	t.Run("empty batch is invalid", func(t *testing.T) {
		t.Parallel()
		b := Batch{Records: nil}
		if err := b.Validate(); err == nil {
			t.Fatal("Validate accepted empty batch")
		}
	})

	t.Run("empty record id is invalid", func(t *testing.T) {
		t.Parallel()
		r := sampleRecord("ok")
		r.RecordID = ""
		b := Batch{Records: []Record{r}}
		if err := b.Validate(); err == nil {
			t.Fatal("Validate accepted empty RecordID")
		}
	})

	t.Run("duplicate record id is invalid", func(t *testing.T) {
		t.Parallel()
		b := Batch{Records: []Record{sampleRecord("a"), sampleRecord("a")}}
		if err := b.Validate(); err == nil {
			t.Fatal("Validate accepted duplicate RecordID")
		}
	})

	t.Run("clean batch validates", func(t *testing.T) {
		t.Parallel()
		b := Batch{Records: []Record{sampleRecord("a"), sampleRecord("b")}}
		if err := b.Validate(); err != nil {
			t.Fatalf("Validate rejected clean batch: %v", err)
		}
	})
}

func TestBatchLeafHashesAndIndexOf(t *testing.T) {
	t.Parallel()

	b := &Batch{Records: []Record{
		sampleRecord("alpha"),
		sampleRecord("beta"),
		sampleRecord("gamma"),
	}}

	leaves := b.LeafHashes()
	if len(leaves) != 3 {
		t.Fatalf("LeafHashes len: got %d, want 3", len(leaves))
	}

	if b.IndexOf("beta") != 1 {
		t.Fatalf("IndexOf(beta): got %d, want 1", b.IndexOf("beta"))
	}
	if b.IndexOf("missing") != -1 {
		t.Fatalf("IndexOf(missing): got %d, want -1", b.IndexOf("missing"))
	}
}

func TestSortByRecordIDDoesNotMutateOriginal(t *testing.T) {
	t.Parallel()

	b := &Batch{Records: []Record{
		sampleRecord("c"),
		sampleRecord("a"),
		sampleRecord("b"),
	}}
	sorted := b.SortByRecordID()

	wantOriginal := []string{"c", "a", "b"}
	for i, r := range b.Records {
		if r.RecordID != wantOriginal[i] {
			t.Fatalf("original mutated at %d: got %s, want %s", i, r.RecordID, wantOriginal[i])
		}
	}

	wantSorted := []string{"a", "b", "c"}
	for i, r := range sorted.Records {
		if r.RecordID != wantSorted[i] {
			t.Fatalf("sorted at %d: got %s, want %s", i, r.RecordID, wantSorted[i])
		}
	}
}

func TestStreamIDIsStable(t *testing.T) {
	t.Parallel()

	if StreamID("audit") != StreamID("audit") {
		t.Fatal("StreamID(audit) is not stable across calls")
	}
	if StreamID("audit") == StreamID("usage") {
		t.Fatal("different stream names produced the same StreamID")
	}
}
