package main

import (
	"strings"
	"testing"

	"github.com/kiff/cloud/api/internal/receipt"
)

func TestResolveReceiptMode(t *testing.T) {
	t.Parallel()

	store := receipt.NewMemoryStore()
	const (
		key      = "0xdeadbeef"
		rpc      = "https://sepolia.base.org"
		contract = "0x2F5468eE0De25c6a86D5F1445F37E6a79039DEa4"
	)

	tests := []struct {
		name     string
		raw      string
		store    receipt.Store
		key      string
		rpc      string
		contract string
		want     receiptMode
		wantErr  string
	}{
		{
			name:  "explicit disabled",
			raw:   "disabled",
			store: store,
			want:  receiptModeDisabled,
		},
		{
			name:  "explicit local with store",
			raw:   "local",
			store: store,
			want:  receiptModeLocal,
		},
		{
			name:    "explicit local without store fails",
			raw:     "local",
			wantErr: "requires a receipt store",
		},
		{
			name:     "explicit anchor with all envs",
			raw:      "anchor",
			store:    store,
			key:      key,
			rpc:      rpc,
			contract: contract,
			want:     receiptModeAnchor,
		},
		{
			name:     "explicit anchor missing key fails",
			raw:      "anchor",
			store:    store,
			rpc:      rpc,
			contract: contract,
			wantErr:  "requires KIFF_ANCHOR_PRIVATE_KEY",
		},
		{
			name: "unset with no store infers disabled",
			raw:  "",
			want: receiptModeDisabled,
		},
		{
			name:  "unset with store and no anchor envs infers local",
			raw:   "",
			store: store,
			want:  receiptModeLocal,
		},
		{
			name:     "unset with store and all anchor envs infers anchor",
			raw:      "",
			store:    store,
			key:      key,
			rpc:      rpc,
			contract: contract,
			want:     receiptModeAnchor,
		},
		{
			name:    "unknown value fails",
			raw:     "wat",
			store:   store,
			wantErr: "unknown KIFF_RECEIPT_MODE value",
		},
		{
			name:  "case insensitive — LOCAL",
			raw:   "LOCAL",
			store: store,
			want:  receiptModeLocal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveReceiptMode(tt.raw, tt.store, tt.key, tt.rpc, tt.contract)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got mode=%q nil", tt.wantErr, got)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("mode = %q, want %q", got, tt.want)
			}
		})
	}
}
