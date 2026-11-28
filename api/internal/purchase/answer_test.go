package purchase

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	kiffauth "github.com/kiff/cloud/api/internal/auth"
)

// KIFF's answer message for a held purchase is the contract's vector, byte
// for byte: the apps sign exactly what KIFF verifies.
func TestThePurchaseAnswerIsTheContractsVector(t *testing.T) {
	raw, err := os.ReadFile("testdata/purchase.json")
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Case struct {
			Value struct {
				CaseID  string `json:"case_id"`
				Version int    `json:"version"`
			} `json:"value"`
			Digest string `json:"digest"`
		} `json:"case"`
		Answer struct {
			Vector struct {
				Tenant      string `json:"tenant"`
				ExceptionID string `json:"exception_id"`
				Answer      string `json:"answer"`
				NextStep    string `json:"next_step"`
				Note        string `json:"note"`
				HoldVersion int    `json:"hold_version"`
				SignedAt    string `json:"signed_at"`
				Message     string `json:"message"`
				SHA256      string `json:"message_sha256"`
			} `json:"vector"`
		} `json:"answer"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	v := d.Answer.Vector
	msg := kiffauth.Answer{TenantID: v.Tenant, ExceptionID: v.ExceptionID, Answer: v.Answer, NextStep: v.NextStep, Note: v.Note,
		AccountingVersion: v.HoldVersion, SignedAt: v.SignedAt, Purchase: AnswerLine(d.Case.Value.CaseID, d.Case.Value.Version, d.Case.Digest)}.Message()
	if string(msg) != v.Message {
		t.Fatalf("KIFF's message:\n%q\nthe contract's:\n%q", msg, v.Message)
	}
	sum := sha256.Sum256(msg)
	if hex.EncodeToString(sum[:]) != v.SHA256 {
		t.Fatal("sha256")
	}
}
