package anchor

import (
	"strconv"
	"testing"
)

func leafFor(s string) Hash { return LeafHash(nil, []byte(s)) }

func TestNewTreeRejectsEmpty(t *testing.T) {
	t.Parallel()

	if _, err := NewTree(nil); err == nil {
		t.Fatal("NewTree(nil) returned no error")
	}
	if _, err := NewTree([]Hash{}); err == nil {
		t.Fatal("NewTree(empty) returned no error")
	}
}

func TestSingleLeafTreeRootEqualsLeaf(t *testing.T) {
	t.Parallel()

	leaf := leafFor("only-record")
	tree, err := NewTree([]Hash{leaf})
	if err != nil {
		t.Fatalf("NewTree: %v", err)
	}
	if got := tree.Root(); got != leaf {
		t.Fatalf("root: got %s, want %s", got, leaf)
	}

	proof, err := tree.ProofForIndex(0)
	if err != nil {
		t.Fatalf("ProofForIndex: %v", err)
	}
	if len(proof) != 0 {
		t.Fatalf("proof length: got %d, want 0", len(proof))
	}
	if !VerifyProof(leaf, proof, tree.Root()) {
		t.Fatal("VerifyProof failed for single-leaf tree")
	}
}

func TestProofVerifiesEveryLeaf(t *testing.T) {
	t.Parallel()

	for _, n := range []int{2, 3, 4, 5, 7, 8, 9, 16, 33} {
		t.Run("n="+strconv.Itoa(n), func(t *testing.T) {
			t.Parallel()

			leaves := make([]Hash, n)
			for i := range leaves {
				leaves[i] = leafFor("rec-" + strconv.Itoa(i))
			}
			tree, err := NewTree(leaves)
			if err != nil {
				t.Fatalf("NewTree: %v", err)
			}

			for i, leaf := range leaves {
				proof, err := tree.ProofForIndex(i)
				if err != nil {
					t.Fatalf("ProofForIndex(%d): %v", i, err)
				}
				if !VerifyProof(leaf, proof, tree.Root()) {
					t.Fatalf("proof for index %d failed; root=%s", i, tree.Root())
				}
			}
		})
	}
}

func TestProofRejectsWrongLeaf(t *testing.T) {
	t.Parallel()

	leaves := []Hash{leafFor("a"), leafFor("b"), leafFor("c"), leafFor("d")}
	tree, err := NewTree(leaves)
	if err != nil {
		t.Fatalf("NewTree: %v", err)
	}

	proof, err := tree.ProofForIndex(0)
	if err != nil {
		t.Fatalf("ProofForIndex: %v", err)
	}

	if VerifyProof(leafFor("not-in-batch"), proof, tree.Root()) {
		t.Fatal("VerifyProof accepted a leaf not in the batch")
	}
}

func TestProofRejectsWrongRoot(t *testing.T) {
	t.Parallel()

	leaves := []Hash{leafFor("x"), leafFor("y")}
	tree, err := NewTree(leaves)
	if err != nil {
		t.Fatalf("NewTree: %v", err)
	}

	proof, err := tree.ProofForIndex(0)
	if err != nil {
		t.Fatalf("ProofForIndex: %v", err)
	}

	tampered := tree.Root()
	tampered[0] ^= 0xff
	if VerifyProof(leaves[0], proof, tampered) {
		t.Fatal("VerifyProof accepted a tampered root")
	}
}

func TestSameLeavesYieldSameRoot(t *testing.T) {
	t.Parallel()

	a := []Hash{leafFor("1"), leafFor("2"), leafFor("3")}
	b := []Hash{leafFor("1"), leafFor("2"), leafFor("3")}

	ta, err := NewTree(a)
	if err != nil {
		t.Fatalf("NewTree a: %v", err)
	}
	tb, err := NewTree(b)
	if err != nil {
		t.Fatalf("NewTree b: %v", err)
	}

	if ta.Root() != tb.Root() {
		t.Fatalf("equal inputs produced different roots: %s vs %s", ta.Root(), tb.Root())
	}
}

func TestProofForIndexOutOfRange(t *testing.T) {
	t.Parallel()

	tree, err := NewTree([]Hash{leafFor("only")})
	if err != nil {
		t.Fatalf("NewTree: %v", err)
	}
	if _, err := tree.ProofForIndex(-1); err == nil {
		t.Fatal("ProofForIndex(-1) returned no error")
	}
	if _, err := tree.ProofForIndex(1); err == nil {
		t.Fatal("ProofForIndex(1) returned no error")
	}
}
