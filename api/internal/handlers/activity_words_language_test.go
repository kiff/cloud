package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"

	"github.com/kiff/cloud/api/internal/proposals"
	"github.com/kiff/cloud/api/internal/tooloutcome"
	"github.com/kiff/cloud/api/internal/words"
)

// A call's line in another language: who answered it in the form that
// language's "by" takes, what the tool did, and why a connection refused
// it, with the tool's own message kept as it was.
func TestACallsLineInALanguage(t *testing.T) {
	eur := &proposals.CallAmount{Value: "45.5", Unit: "EUR"}
	call := func(outcome string, held bool, o *tooloutcome.Outcome, answer *proposals.CallAnswer) proposals.CallRow {
		r := proposals.CallRow{Held: held, Answer: answer}
		r.ToolOutcome = o
		r.ActionName, r.Title, r.Outcome, r.Amount = "create_refund", "Refund a payment", outcome, eur
		return r
	}
	for _, c := range []struct {
		lang         string
		row          proposals.CallRow
		kind, detail string
		amount       string
	}{
		{"de", call("allowed", false, &tooloutcome.Outcome{Status: tooloutcome.Done}, nil), "done", "Erledigt, im Rahmen seiner Card", "45,50 €"},
		{"de", call("allowed", true, &tooloutcome.Outcome{Status: tooloutcome.Done}, &proposals.CallAnswer{By: "approver"}), "approved", "Genehmigt von einem Genehmiger, dann erledigt", "45,50 €"},
		{"fr", call("owner_required", true, nil, &proposals.CallAnswer{Decision: "approved_once", You: true}), "approved", "Approuvé une fois par vous", "45,50 €"},
		{"pl", call("owner_required", true, nil, &proposals.CallAnswer{Decision: "refused", By: "approver"}), "refused", "Odrzucone przez osobę zatwierdzającą", "45,50 €"},
		{"es", call("owner_required", true, nil, nil), "held", "Esperando tu respuesta", "45,50 €"},
		{"it", call("owner_required", true, nil, &proposals.CallAnswer{Decision: "change-card", By: "owner"}), "approved", "Margine extra concesso dal proprietario", "45,50\u00a0€"},
		{"nl", call("allowed", false, &tooloutcome.Outcome{Status: tooloutcome.NotSent, Message: "timeout"}, nil), "not_done",
			"Toegestaan door de Card, maar het kon niet worden verzonden: er kwam niets bij de tool aan", "€ 45,50"},
		{"fr", call("allowed", false, &tooloutcome.Outcome{Status: tooloutcome.RefusedByTool, RefusedBy: "connection", Code: "not_granted"}, nil), "not_done",
			"Non envoyé : la connexion l'a refusé avant l'envoi : la connexion n'inclut pas cette action. Il compte encore dans les limites du jour jusqu'à ce que KIFF puisse confirmer que rien n'a été fait.", "45,50 €"},
		{"it", call("allowed", false, &tooloutcome.Outcome{Status: tooloutcome.RefusedByTool}, nil), "not_done",
			"L'app l'ha rifiutata. Conta nel totale di oggi finché KIFF non può confermare che non si è mosso denaro.", "45,50 €"},
		{"pt", call("allowed", true, &tooloutcome.Outcome{Status: tooloutcome.Unknown}, &proposals.CallAnswer{By: "owner"}), "unknown",
			"Aprovada pelo proprietário, e enviada, mas a resposta da ferramenta perdeu-se: verifique os registos da própria ferramenta", "45,50 €"},
		// Found at the vendor by the status read (kiff/tafs#498): done, and says how KIFF knows.
		{"fr", call("allowed", false, &tooloutcome.Outcome{Status: tooloutcome.Done, Found: true}, nil), "done",
			"Autorisé par sa Card, et envoyé. KIFF ne savait pas comment cela s'était terminé ; il a vérifié les enregistrements de l'outil lui-même et l'y a trouvé : c'est passé", "45,50 €"},
		{"de", call("allowed", true, &tooloutcome.Outcome{Status: tooloutcome.Done, Found: true}, &proposals.CallAnswer{By: "approver"}), "done",
			"Genehmigt von einem Genehmiger, und gesendet. KIFF konnte nicht sagen, wie es ausging, hat in den eigenen Aufzeichnungen des Tools nachgesehen und es dort gefunden: Es ist durchgegangen", "45,50 €"},
	} {
		r := c.row
		withCallWords(&r, wordsIn(c.lang))
		if r.Kind != c.kind || r.Detail != c.detail || r.AmountText != c.amount {
			t.Errorf("%s: %q %q %q\nwant %q %q %q", c.lang, r.Kind, r.Detail, r.AmountText, c.kind, c.detail, c.amount)
		}
	}
	// Every connection refusal code has its reason in every language.
	for _, l := range words.All {
		for code := range words.Of(words.EN).ConnectionWhy {
			if strings.TrimSpace(words.Of(l).ConnectionWhy[code]) == "" {
				t.Errorf("%s: no reason for %s", l, code)
			}
		}
	}
}

// Answering a hold returns its Record line as the answerer's app shows it,
// in that app's language.
func TestAnAnswersRecordLineInTheAnswerersLanguage(t *testing.T) {
	mux, store := mandatesServer(t, owner())
	ctx := context.Background()
	card := mandate.Mandate{ID: "card-x", TenantID: "t1", Subject: "agent://a", OnExceed: mandate.OnExceedOwner,
		Grants: []mandate.Grant{{Action: "REFUND_INVOICE", PerAction: []mandate.PerActionLimit{{
			Quantity: mandate.Quantity{Kind: mandate.KindSum, Parameter: "amount"}, Max: 100}}}}}
	if err := store.Issue(ctx, card, "owner"); err != nil {
		t.Fatal(err)
	}
	a := mandate.NewAuthorizer(store, nil, func() time.Time { return time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC) })
	d, err := a.Authorize(ctx, "t1", "p-1", mandate.Request{Subject: "agent://a", Action: "REFUND_INVOICE", Parameters: map[string]int64{"amount": 500}})
	if err != nil || d.Outcome != mandate.OutcomeOwnerRequired {
		t.Fatalf("hold: %+v %v", d, err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/me/card-exceptions/"+d.ExceptionID+"/approve-once", nil)
	req.Header.Set("Accept-Language", "fr-FR")
	req = req.WithContext(auth.WithIdentity(req.Context(), owner()))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	var v struct {
		Record map[string]string `json:"record"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &v); err != nil || rr.Code != http.StatusOK {
		t.Fatalf("%d %v %s", rr.Code, err, rr.Body.String())
	}
	if v.Record["kind"] != "approved" || v.Record["detail"] != "Approuvé une fois par vous" {
		t.Errorf("record = %v", v.Record)
	}
}
