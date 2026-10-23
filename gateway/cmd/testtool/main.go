// Command testtool is a remote MCP server with two tools: refund, which
// acts, and quote, which only reads. It logs and numbers every call it
// receives. It stands in for a connected tool in local runs of the
// gateway and the smoke test.
//
// It is a local fixture only and must never be deployed: GET /calls is
// unauthenticated, and the tools do nothing real.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/kiff/cloud/gateway/internal/mcp"
)

type refunds struct {
	n             atomic.Int64
	finalRefusals sync.Map
}

func (r *refunds) ListTools(context.Context) ([]mcp.Tool, error) {
	return []mcp.Tool{{Name: "refund", Description: "Refund an order",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"order":{"type":"string"},"amount_eur":{"type":"integer"},"idempotency_key":{"type":"string"}},"required":["order","amount_eur"]}`)},
		{Name: "refund_status", Description: "Read the final status of a refund; never sends a refund", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
			InputSchema: json.RawMessage(`{"type":"object","properties":{"idempotency_key":{"type":"string"}},"required":["idempotency_key"]}`)},
		{Name: "quote", Description: "Quote what refunding an order would cost; changes nothing",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"order":{"type":"string"},"amount_eur":{"type":"integer"}},"required":["order","amount_eur"]}`)}}, nil
}

func (r *refunds) CallTool(_ context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	if name == "refund_status" {
		var a struct {
			Operation string `json:"idempotency_key"`
		}
		_ = json.Unmarshal(args, &a)
		if _, exists := r.finalRefusals.Load(a.Operation); exists && a.Operation != "" {
			return finalNoEffect(a.Operation, false), nil
		}
		return mcp.TextResult("Outcome unknown; absence is not evidence of no effect.", false), nil
	}
	var label string
	switch name {
	case "refund":
		label = "Refund #%d issued: %s"
	case "quote":
		label = "Quote #%d: %s"
	default:
		return nil, mcp.UnknownTool(name)
	}
	// One counter for both tools: the smoke test reads how many calls
	// reached the tool, whichever it was.
	n := r.n.Add(1)
	log.Printf("call %d: %s %s", n, name, args)
	// An order named declined-… is received and refused by the tool, as a
	// payment provider refuses a refund.
	var a struct {
		Order     string `json:"order"`
		Operation string `json:"idempotency_key"`
	}
	_ = json.Unmarshal(args, &a)
	if strings.HasPrefix(a.Order, "no-effect-") {
		// This fixture promises the operation is finally refused and will
		// never execute later. Generic declined-* errors make no such claim.
		r.finalRefusals.Store(a.Operation, true)
		return finalNoEffect(a.Operation, true), nil
	}
	if strings.HasPrefix(a.Order, "declined-") {
		return mcp.TextResult(fmt.Sprintf("Refund declined for order %s: amount is greater than what is left to refund.", a.Order), true), nil
	}
	return mcp.TextResult(fmt.Sprintf(label, n, args), false), nil
}

func finalNoEffect(operation string, isError bool) json.RawMessage {
	out, _ := json.Marshal(map[string]any{
		"content": []any{map[string]any{"type": "text", "text": "Final refusal: no business effect."}}, "isError": isError,
		"structuredContent": map[string]any{"kiff_outcome": map[string]any{"operation_id": operation, "effect": "none", "terminal": true, "reason_code": "fixture_final_refusal"}},
	})
	return out
}

// loseResponse simulates an executor retaining a final refusal while the
// caller loses its response. The separate status tool can then prove the
// outcome without sending the business action again. Local fixture only.
func (r *refunds) loseResponse(next http.Handler, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost && (token == "" || req.Header.Get("Authorization") == "Bearer "+token) {
			body, err := io.ReadAll(io.LimitReader(req.Body, 1<<20))
			if err == nil {
				req.Body = io.NopCloser(bytes.NewReader(body))
				var call struct {
					Method string `json:"method"`
					Params struct {
						Name      string `json:"name"`
						Arguments struct {
							Order     string `json:"order"`
							Operation string `json:"idempotency_key"`
						} `json:"arguments"`
					} `json:"params"`
				}
				if json.Unmarshal(body, &call) == nil && call.Method == "tools/call" && call.Params.Name == "refund" && strings.HasPrefix(call.Params.Arguments.Order, "unknown-no-effect-") {
					r.n.Add(1)
					r.finalRefusals.Store(call.Params.Arguments.Operation, true)
					if hijacker, ok := w.(http.Hijacker); ok {
						conn, _, err := hijacker.Hijack()
						if err == nil {
							_ = conn.Close()
							return
						}
					}
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
			}
		}
		next.ServeHTTP(w, req)
	})
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8383", "listen address")
	token := flag.String("token", "", "bearer token callers must present; empty accepts any caller")
	flag.Parse()
	h := &refunds{}
	mux := http.NewServeMux()
	// GET /calls is how many calls the tools have received: the smoke
	// test asserts on it, not on what the
	// agent was told.
	mux.HandleFunc("GET /calls", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]int64{"calls": h.n.Load()})
	})
	mux.Handle("/", h.loseResponse(&mcp.Server{Name: "testtool", Version: "1",
		Auth: func(r *http.Request) (mcp.Handler, error) {
			if *token != "" && r.Header.Get("Authorization") != "Bearer "+*token {
				log.Printf("refused a caller without the token")
				return nil, errors.New("bad token")
			}
			return h, nil
		}}, *token))
	log.Printf("testtool listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
