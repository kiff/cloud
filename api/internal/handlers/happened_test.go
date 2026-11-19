package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/access"
	"github.com/kiff/cloud/api/internal/agents"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/tenant"
	"github.com/kiff/cloud/api/internal/tooloutcome"
)

type happenedFake struct {
	mu       sync.Mutex
	answer   *tooloutcome.HappenedAnswer
	checking *tooloutcome.Checking
	// calls are the unproven calls listed; nil lists the one call p1.
	calls []tooloutcome.Unproven
	now   time.Time
}

func (f *happenedFake) Outcomes(context.Context, string, []string) (map[string]tooloutcome.Outcome, error) {
	return nil, nil
}
func (f *happenedFake) Unproven(context.Context, string, string) ([]tooloutcome.Unproven, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]tooloutcome.Unproven{}, f.calls...)
	for i := range out {
		if f.checking != nil && f.checking.Until.After(f.now) {
			out[i].Checking = f.checking
		}
	}
	return out, nil
}
func (f *happenedFake) UnprovenCall(_ context.Context, _, proposal string) (tooloutcome.Unproven, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c.Proposal == proposal {
			return c, true, nil
		}
	}
	if f.calls != nil {
		return tooloutcome.Unproven{}, false, nil
	}
	return tooloutcome.Unproven{Proposal: "p1", Agent: "agent-a", Tool: "refund"}, true, nil
}
func (f *happenedFake) MarkChecking(_ context.Context, _, _, by string, until time.Time) (tooloutcome.Checking, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.checking == nil || !f.checking.Until.After(f.now) {
		f.checking = &tooloutcome.Checking{By: by, Until: until}
	}
	return *f.checking, nil
}
func (f *happenedFake) RecordHappened(_ context.Context, _, _ string, a tooloutcome.HappenedAnswer) (tooloutcome.HappenedAnswer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.answer == nil {
		f.answer = &a
	}
	return *f.answer, nil
}

type releasesFake struct {
	mu       sync.Mutex
	released int
	fail     int
}

func (r *releasesFake) ReleaseEffects(context.Context, mandate.NoEffectEvidence) ([]mandate.EffectRelease, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail > 0 {
		r.fail--
		return nil, errors.New("down")
	}
	r.released++
	return nil, nil
}
func (r *releasesFake) EffectReleases(context.Context, string, string) ([]mandate.EffectRelease, error) {
	return nil, nil
}

// The first answer stands, and only a kept No gives the Card its room back:
// a No that loses to a Yes releases nothing, however they race.
func TestAKeptYesKeepsTheCardCharged(t *testing.T) {
	at := time.Now()
	for i := 0; i < 50; i++ {
		s, rs := &happenedFake{}, &releasesFake{}
		var wg sync.WaitGroup
		for _, yes := range []bool{true, false} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _, _ = answerFirst(context.Background(), s, rs, "t1", "p1", tooloutcome.HappenedAnswer{Happened: yes, AnsweredBy: "o", AnsweredAt: at})
			}()
		}
		wg.Wait()
		if s.answer.Happened && rs.released != 0 {
			t.Fatalf("Yes was kept, but the room came back %d times", rs.released)
		}
		if !s.answer.Happened && rs.released == 0 {
			t.Fatal("No was kept, but no room came back")
		}
	}
}

// A No whose release fails is kept, and the next ask finishes the release
// without changing the answer.
func TestAKeptNoWhoseReleaseFailedFinishesOnTheNextAsk(t *testing.T) {
	s, rs := &happenedFake{}, &releasesFake{fail: 1}
	no := tooloutcome.HappenedAnswer{Happened: false, AnsweredBy: "o", AnsweredAt: time.Now()}
	if _, _, err := answerFirst(context.Background(), s, rs, "t1", "p1", no); !errors.Is(err, errRoomNotBack) {
		t.Fatalf("first ask: %v", err)
	}
	yes := tooloutcome.HappenedAnswer{Happened: true, AnsweredBy: "x", AnsweredAt: time.Now()}
	kept, found, err := answerFirst(context.Background(), s, rs, "t1", "p1", yes)
	if err != nil || !found || kept.Happened || rs.released != 1 {
		t.Fatalf("next ask: %+v %v %v, released %d", kept, found, err, rs.released)
	}
}

// refundHappenedFake is a store whose calls thune governs: the owner's No
// hands the refund's request back, and that can fail.
type refundHappenedFake struct {
	happenedFake
	fail  int
	freed int
}

func (f *refundHappenedFake) OwnerSaidNotSent(context.Context, string, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail > 0 {
		f.fail--
		return errors.New("down")
	}
	f.freed++
	return nil
}

// A No on a governed refund whose request couldn't be handed back says so,
// not that the room didn't come back; the next ask hands it back, gives no
// room twice, and keeps the answer.
func TestAKeptNoWhoseRequestWasntHandedBackFinishesOnTheNextAsk(t *testing.T) {
	s, rs := &refundHappenedFake{fail: 1}, &releasesFake{}
	no := tooloutcome.HappenedAnswer{Happened: false, AnsweredBy: "o", AnsweredAt: time.Now()}
	if _, _, err := answerFirst(context.Background(), s, rs, "t1", "p1", no); !errors.Is(err, errRequestNotReleased) || rs.released != 1 {
		t.Fatalf("first ask: %v, released %d", err, rs.released)
	}
	kept, found, err := answerFirst(context.Background(), s, rs, "t1", "p1", no)
	if err != nil || !found || kept.Happened || s.freed != 1 {
		t.Fatalf("next ask: %+v %v %v, freed %d", kept, found, err, s.freed)
	}
	if rs.released != 2 {
		t.Fatalf("released %d: each ask asks for the release once, and the store keeps one per draw", rs.released)
	}
}

// happenedRig mounts "Did this happen?" with an account in Madrid, a taf
// named "Refunds 4", and one call to confirm: a refund at Stripe whose
// answer was lost, on payment pi_1, sent at 09:14 UTC today.
type happenedRig struct {
	mux    *http.ServeMux
	fake   *happenedFake
	now    time.Time
	tnt    string
	own    *OwnUse
	agent  string
	agents agents.Store
}

func newHappenedRig(t *testing.T, opts ...MandatesOption) *happenedRig {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 11, 5, 11, 0, 0, 0, time.UTC)
	tenants := tenant.NewMemoryStore()
	tnt := tenant.Tenant{ID: tenant.NewID(), Slug: "happened", Status: tenant.StatusActive}
	if err := tenants.Create(ctx, tnt); err != nil {
		t.Fatal(err)
	}
	if err := tenants.SetMetadata(ctx, tnt.ID, tenant.MetadataKeyTimeZone, "Europe/Madrid"); err != nil {
		t.Fatal(err)
	}
	agentStore := agents.NewMemoryStore()
	if err := agentStore.Create(ctx, agents.Agent{TenantID: tnt.ID, ID: "agent-a", Kind: agents.KindTaf, CreatedAt: now, CreatedBy: "alice", Name: "Refunds 4"}); err != nil {
		t.Fatal(err)
	}
	fake := &happenedFake{now: now, calls: []tooloutcome.Unproven{{
		Proposal: "p1", Agent: "agent-a", Tool: "refund", Title: "Refund a payment", Status: tooloutcome.Unknown,
		Amount: 40000, Unit: "EUR", Scale: 2, At: now.Add(-106 * time.Minute), Words: "Refund a payment: 400.00 EUR.",
		StatusWords: "It was sent, but its answer was lost, so KIFF can't tell whether it happened.",
		Vendor:      "Stripe", Counterparty: &tooloutcome.Counterparty{Noun: "payment", ID: "pi_1"},
		RecordURL: "https://dashboard.stripe.com/payments/pi_1",
	}}}
	mux := http.NewServeMux()
	// Who manages each taf is read through OwnUse: an admin reads the whole account, so empty stores do here.
	own := NewOwnUse(NewAccessHandler(access.NewMemoryStore(), membership.NewMemoryStore(), agentStore, nil), askedBy{})
	opts = append([]MandatesOption{WithMandatesToolOutcomes(fake), WithMandatesAgents(agentStore), WithMandatesTenants(tenants),
		WithMandatesClock(func() time.Time { return now }), WithMandatesOwnUse(own)}, opts...)
	NewMandatesHandler(mandate.NewMemoryStore(), nil, opts...).Register(mux)
	return &happenedRig{mux: mux, fake: fake, now: now, tnt: tnt.ID, agent: "agent-a", agents: agentStore}
}

func (r *happenedRig) do(t *testing.T, id auth.Identity, method, path, body, lang string) (int, map[string]any) {
	t.Helper()
	id.Tenant = r.tnt
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if lang != "" {
		req.Header.Set("Accept-Language", lang)
	}
	req = req.WithContext(auth.WithIdentity(req.Context(), id))
	rec := httptest.NewRecorder()
	r.mux.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func (r *happenedRig) calls(t *testing.T, lang string) []map[string]any {
	t.Helper()
	code, out := r.do(t, admin(), http.MethodGet, "/v1/me/did-this-happen", "", lang)
	if code != http.StatusOK {
		t.Fatalf("list: %d %v", code, out)
	}
	raw, _ := out["calls"].([]any)
	var calls []map[string]any
	for _, c := range raw {
		calls = append(calls, c.(map[string]any))
	}
	return calls
}

// The list says which taf made the call, at which vendor, on what, when
// (in the account's zone), why KIFF can't tell, where to look, and the
// three answers with their keys, in the reader's language.
func TestDidThisHappenCarriesItsContext(t *testing.T) {
	t.Parallel()
	r := newHappenedRig(t)
	calls := r.calls(t, "")
	if len(calls) != 1 {
		t.Fatalf("calls: %v", calls)
	}
	c := calls[0]
	taf, _ := c["taf"].(map[string]any)
	cp, _ := c["counterparty"].(map[string]any)
	if taf["agent"] != "agent-a" || taf["name"] != "Refunds 4" || c["vendor"] != "Stripe" || cp["noun"] != "payment" || cp["id"] != "pi_1" ||
		c["record_url"] != "https://dashboard.stripe.com/payments/pi_1" || c["amount_words"] != "400.00 EUR" || c["when_words"] != "today 10:14" ||
		c["why"] != "It was sent, but its answer was lost, and Stripe's records can't be searched for this call." || c["words"] != "Refund a payment: 400.00 EUR." {
		t.Fatalf("context: %v", c)
	}
	answers, _ := c["answers"].([]any)
	if len(answers) != 3 {
		t.Fatalf("answers: %v", answers)
	}
	for i, want := range [][2]string{{"yes", "window"}, {"no", "critical"}, {"not_sure", "device"}} {
		a := answers[i].(map[string]any)
		if a["answer"] != want[0] || a["key"] != want[1] || a["words"] == "" || a["detail"] == "" {
			t.Fatalf("answer %d: %v", i, a)
		}
	}
	if _, has := c["checking"]; has {
		t.Fatalf("checking before anyone asked: %v", c)
	}
	es := r.calls(t, "es")[0]
	if es["amount_words"] != "400,00 EUR" || es["when_words"] != "hoy 10:14" || !strings.HasPrefix(es["why"].(string), "Se envió, pero su respuesta se perdió, y en los registros de Stripe") ||
		es["answers"].([]any)[2].(map[string]any)["words"] != "No estoy seguro, ayúdame a comprobarlo" {
		t.Fatalf("es: %v", es)
	}
	// The status line and the call's line are in the reader's language too: what's known and what to check.
	if c["status_words"] != "It was sent to Stripe, but its answer was lost. Check in Stripe whether it happened." {
		t.Fatalf("status_words: %q", c["status_words"])
	}
	if es["status_words"] != "Se envió a Stripe, pero su respuesta se perdió. Comprueba en Stripe si se hizo." || es["words"] != "Refund a payment: 400,00 EUR." {
		t.Fatalf("es status_words %q, words %q", es["status_words"], es["words"])
	}
}

// A call the vendor refused says so, and what to check, in every language: never "can't confirm that no
// money moved".
func TestDidThisHappenStatusSaysWhatIsKnown(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		lang string
		c    tooloutcome.Unproven
		want string
	}{
		{"en", tooloutcome.Unproven{Status: tooloutcome.RefusedByTool, Vendor: "Stripe"}, "Stripe refused it. Check in Stripe whether any money moved."},
		{"fr", tooloutcome.Unproven{Status: tooloutcome.RefusedByTool, Vendor: "Stripe"}, "Stripe l'a refusé. Vérifiez dans Stripe si de l'argent a bougé."},
		{"de", tooloutcome.Unproven{Status: tooloutcome.Unknown, Vendor: "Stripe"}, "Er wurde an Stripe gesendet, aber die Antwort ging verloren. Prüfe bei Stripe, ob er durchgegangen ist."},
		{"pl", tooloutcome.Unproven{Status: tooloutcome.Probation}, "Jedno z pierwszych wywołań przez to połączenie: sprawdź, czy zrobiło to, co miało zrobić."},
		{"nl", tooloutcome.Unproven{Status: tooloutcome.RefusedByTool, Vendor: "Stripe"}, "Stripe heeft hem geweigerd. Kijk in Stripe of er geld is overgemaakt."},
		{"pt", tooloutcome.Unproven{Status: tooloutcome.Unknown, Vendor: "Stripe"}, "Foi enviada para Stripe, mas a resposta perdeu-se. Verifique em Stripe se foi feita."},
	} {
		if got := happenedStatus(tc.c, tc.lang); got != tc.want {
			t.Fatalf("%s %+v: %q, want %q", tc.lang, tc.c, got, tc.want)
		}
	}
	// With no vendor named, each language says a whole sentence: never the
	// fallback noun slotted after a preposition ("bei der Anbieter", "à le").
	for _, tc := range []struct{ lang, refused, lost string }{
		{"en", "The app refused it. Check with the app whether any money moved.", "It was sent, but its answer was lost. Check with the app whether it happened."},
		{"de", "Der Anbieter hat ihn abgelehnt. Prüfe beim Anbieter, ob Geld geflossen ist.", "Er wurde an den Anbieter gesendet, aber die Antwort ging verloren. Prüfe beim Anbieter, ob er durchgegangen ist."},
		{"fr", "Le fournisseur l'a refusé. Vérifiez auprès du fournisseur si de l'argent a bougé.", "Il a été envoyé au fournisseur, mais sa réponse s'est perdue. Vérifiez auprès du fournisseur s'il est passé."},
		{"es", "El proveedor la rechazó. Comprueba con el proveedor si se movió dinero.", "Se envió al proveedor, pero su respuesta se perdió. Comprueba con el proveedor si se hizo."},
		{"it", "Il fornitore l'ha rifiutata. Controlla presso il fornitore se è stato mosso denaro.", "È stata inviata al fornitore, ma la risposta è andata persa. Controlla presso il fornitore se è andata a buon fine."},
		{"nl", "De leverancier heeft hem geweigerd. Kijk bij de leverancier of er geld is overgemaakt.", "De aanroep is naar de leverancier gestuurd, maar het antwoord ging verloren. Kijk bij de leverancier of het is gelukt."},
		{"pl", "Dostawca je odrzucił. Sprawdź u dostawcy, czy pobrano jakieś pieniądze.", "Zostało wysłane do dostawcy, ale odpowiedź zginęła. Sprawdź u dostawcy, czy przeszło."},
		{"pt", "O fornecedor recusou-a. Verifique junto do fornecedor se algum dinheiro foi movimentado.", "Foi enviada para o fornecedor, mas a resposta perdeu-se. Verifique junto do fornecedor se foi feita."},
	} {
		if got := happenedStatus(tooloutcome.Unproven{Status: tooloutcome.RefusedByTool}, tc.lang); got != tc.refused {
			t.Fatalf("%s refused, no vendor: %q, want %q", tc.lang, got, tc.refused)
		}
		if got := happenedStatus(tooloutcome.Unproven{Status: tooloutcome.Unknown}, tc.lang); got != tc.lost {
			t.Fatalf("%s lost, no vendor: %q, want %q", tc.lang, got, tc.lost)
		}
	}
	for _, lang := range []string{"en", "de", "fr", "es", "it", "pt", "nl", "pl"} {
		if got := happenedStatus(tooloutcome.Unproven{Status: tooloutcome.RefusedByTool, Vendor: "Stripe"}, lang); strings.Contains(got, "KIFF") {
			t.Fatalf("%s: the status names KIFF: %q", lang, got)
		}
	}
}

// When and why read from the record: yesterday, a day and month, a
// refusal, a profile with a status read, and no vendor.
func TestDidThisHappenWords(t *testing.T) {
	t.Parallel()
	madrid, _ := time.LoadLocation("Europe/Madrid")
	now := time.Date(2026, 11, 5, 11, 0, 0, 0, time.UTC)
	for _, tc := range []struct{ at, want, es string }{
		{"2026-11-05T09:14:00Z", "today 10:14", "hoy 10:14"},
		{"2026-11-04T23:30:00Z", "today 00:30", "hoy 00:30"},
		{"2026-11-04T17:02:00Z", "yesterday 18:02", "ayer 18:02"},
		{"2026-11-03T08:00:00Z", "3 Nov 09:00", "3 nov 09:00"},
	} {
		at, _ := time.Parse(time.RFC3339, tc.at)
		if got := whenWords(at, now, madrid, langEN); got != tc.want {
			t.Fatalf("%s: %q", tc.at, got)
		}
		if got := whenWords(at, now, madrid, langES); got != tc.es {
			t.Fatalf("%s es: %q", tc.at, got)
		}
	}
	for _, tc := range []struct {
		c    tooloutcome.Unproven
		want string
	}{
		{tooloutcome.Unproven{Status: tooloutcome.RefusedByTool, Vendor: "Stripe"}, "Stripe refused it without saying whether anything was taken, and its records can't be searched for this call."},
		{tooloutcome.Unproven{Status: tooloutcome.RefusedByTool, Vendor: "Stripe", HasStatusRead: true}, "Stripe refused it without saying whether anything was taken, and KIFF's reads of its records haven't settled it."},
		{tooloutcome.Unproven{Status: tooloutcome.Unknown, HasStatusRead: true}, "It was sent, but its answer was lost, and KIFF's reads of the vendor's records haven't settled it."},
		{tooloutcome.Unproven{Status: tooloutcome.RefusedByTool}, "The vendor refused it without saying whether anything was taken, and its records can't be searched for this call."},
		{tooloutcome.Unproven{Status: tooloutcome.Probation, Vendor: "Stripe"}, "One of the first calls of a connection KIFF hasn't tried with a test account: confirm what happened."},
	} {
		if got := happenedWhy(tc.c, langEN); got != tc.want {
			t.Fatalf("why %+v: %q", tc.c, got)
		}
	}
}

// "Not sure" (the device key's route, or the answer on the signed one)
// marks the call as being checked for a day, answers what the app hands
// Coach, and keeps the first mark while it holds; the list shows it.
func TestNotSureKeepsTheAskWithWhoAskedForHelp(t *testing.T) {
	t.Parallel()
	r := newHappenedRig(t)
	code, out := r.do(t, admin(), http.MethodPost, "/v1/me/did-this-happen/p1/check", "", "")
	if code != http.StatusOK {
		t.Fatalf("check: %d %v", code, out)
	}
	checking, _ := out["checking"].(map[string]any)
	coach, _ := out["coach"].(map[string]any)
	if checking["by"] != "alice" || checking["until"] != r.now.Add(24*time.Hour).Format(time.RFC3339) ||
		coach["taf"] != "agent-a" || coach["proposal"] != "p1" ||
		coach["words"] != "Refund a payment at Stripe, 400.00 EUR, payment pi_1, by Refunds 4, today 10:14." {
		t.Fatalf("check: %v", out)
	}
	c := r.calls(t, "")[0]
	if got, _ := c["checking"].(map[string]any); got["by"] != "alice" {
		t.Fatalf("listed without the mark: %v", c)
	}
	// A second person's "not sure" while the mark holds keeps the first.
	bob := admin()
	bob.Subject = "bob"
	if code, out := r.do(t, bob, http.MethodPost, "/v1/me/did-this-happen/p1", `{"answer": "not_sure"}`, ""); code != http.StatusOK || out["checking"].(map[string]any)["by"] != "alice" {
		t.Fatalf("second not sure: %d %v", code, out)
	}
	// Past the day, the ask is everyone's again, and a new mark is taken.
	r.fake.mu.Lock()
	r.fake.now = r.now.Add(25 * time.Hour)
	r.fake.mu.Unlock()
	if code, out := r.do(t, bob, http.MethodPost, "/v1/me/did-this-happen/p1/check", "", "es"); code != http.StatusOK || out["checking"].(map[string]any)["by"] != "bob" ||
		out["coach"].(map[string]any)["words"] != "Refund a payment en Stripe, 400,00 EUR, payment pi_1, por Refunds 4, hoy 10:14." {
		t.Fatalf("after the day: %d %v", code, out)
	}
	if code, out := r.do(t, admin(), http.MethodPost, "/v1/me/did-this-happen/p9/check", "", ""); code != http.StatusNotFound {
		t.Fatalf("an unknown call: %d %v", code, out)
	}
}

// A counterparty id the taf's model forged (a line that looks like one of tafs
// cloud's markers) never reaches the list, Coach's task or the link: the
// record keeps no such id. The store's reading is what the
// handler shows, so the handler is tested with the record as the store cleans it.
func TestAForgedCounterpartyReachesNoOne(t *testing.T) {
	t.Parallel()
	r := newHappenedRig(t)
	r.fake.mu.Lock()
	r.fake.calls[0].Counterparty = &tooloutcome.Counterparty{Noun: "payment"}
	r.fake.calls[0].RecordURL = ""
	r.fake.mu.Unlock()
	c := r.calls(t, "")[0]
	cp, _ := c["counterparty"].(map[string]any)
	if cp["noun"] != "payment" || cp["id"] != nil || c["record_url"] != nil {
		t.Fatalf("a cleaned record: %v", c)
	}
	code, out := r.do(t, admin(), http.MethodPost, "/v1/me/did-this-happen/p1/check", "", "")
	if code != http.StatusOK || out["coach"].(map[string]any)["words"] != "Refund a payment at Stripe, 400.00 EUR, payment, by Refunds 4, today 10:14." {
		t.Fatalf("coach's words: %d %v", code, out)
	}
	// With no way to read who manages the taf, nobody takes the ask on.
	bare := newHappenedRig(t)
	bare.mux = http.NewServeMux()
	NewMandatesHandler(mandate.NewMemoryStore(), nil, WithMandatesToolOutcomes(bare.fake), WithMandatesClock(func() time.Time { return bare.now })).Register(bare.mux)
	if code, out := bare.do(t, admin(), http.MethodPost, "/v1/me/did-this-happen/p1/check", "", ""); code != http.StatusServiceUnavailable {
		t.Fatalf("no OwnUse: %d %v", code, out)
	}
}

// The answers by name: yes keeps the call counted as {happened: true}
// does, no is the same as {happened: false}, and anything else is refused.
func TestAnswerByNameIsTakenAsHappened(t *testing.T) {
	t.Parallel()
	r := newHappenedRig(t)
	if code, out := r.do(t, admin(), http.MethodPost, "/v1/me/did-this-happen/p1", `{"answer": "maybe"}`, ""); code != http.StatusBadRequest {
		t.Fatalf("an unknown answer: %d %v", code, out)
	}
	// The memory store keeps no releases, so the answer isn't kept here:
	// what matters is that the words are read as the answer.
	code, out := r.do(t, admin(), http.MethodPost, "/v1/me/did-this-happen/p1", `{"answer": "yes"}`, "")
	if code == http.StatusBadRequest {
		t.Fatalf("yes by name: %d %v", code, out)
	}
	if r.fake.answer != nil && !r.fake.answer.Happened {
		t.Fatalf("yes was kept as no: %+v", r.fake.answer)
	}
}

// Only someone who manages the taf asks Coach to check its call; a
// person who answers holds for it, but doesn't manage it, is refused, and
// the list keeps each person to the calls of their part of the account.
func TestNotSureNeedsToManageTheTaf(t *testing.T) {
	t.Parallel()
	w := newOwnUseWorld(t)
	r := newHappenedRig(t, WithMandatesOwnUse(w.own))
	r.tnt = "acme"
	r.fake.mu.Lock()
	r.fake.calls[0].Agent = "refunds"
	r.fake.mu.Unlock()
	maria := auth.Identity{Subject: "maria", Method: auth.MethodSession, MemberRole: membership.RoleApprover, Roles: membership.IdentityRoles(membership.RoleApprover)}
	ed := auth.Identity{Subject: "ed", Method: auth.MethodSession, MemberRole: membership.RoleEditor, Roles: membership.IdentityRoles(membership.RoleEditor)}
	if code, out := r.do(t, maria, http.MethodPost, "/v1/me/did-this-happen/p1/check", "", ""); code != http.StatusForbidden {
		t.Fatalf("maria, who uses refunds but doesn't manage it: %d %v", code, out)
	}
	if code, out := r.do(t, ed, http.MethodPost, "/v1/me/did-this-happen/p1/check", "", ""); code != http.StatusOK || out["checking"].(map[string]any)["by"] != "ed" {
		t.Fatalf("ed, who manages refunds: %d %v", code, out)
	}
	// maria answers holds for refunds, so she reads its ask; a call of a
	// taf she has nothing of isn't hers.
	r.fake.mu.Lock()
	r.fake.calls = append(r.fake.calls, tooloutcome.Unproven{Proposal: "p2", Agent: "bills", Tool: "refund", Amount: 100, Unit: "EUR", Scale: 2, At: r.now})
	r.fake.mu.Unlock()
	code, out := r.do(t, maria, http.MethodGet, "/v1/me/did-this-happen", "", "")
	calls, _ := out["calls"].([]any)
	if code != http.StatusOK || len(calls) != 1 || calls[0].(map[string]any)["proposal"] != "p1" {
		t.Fatalf("maria's list: %d %v", code, out)
	}
}

// A removed taf's asks leave the list: its agent is archived, its Cards are
// revoked with it, and nobody can act on them. The records stay, nothing is
// answered for the owner, and another taf's ask is still listed.
func TestDidThisHappenLeavesOutARemovedTafsAsks(t *testing.T) {
	t.Parallel()
	r := newHappenedRig(t)
	ctx := context.Background()
	if err := r.agents.Create(ctx, agents.Agent{TenantID: r.tnt, ID: "agent-b", Kind: agents.KindTaf, CreatedAt: r.now, CreatedBy: "alice", Name: "Chargebacks"}); err != nil {
		t.Fatal(err)
	}
	r.fake.calls = append(r.fake.calls, tooloutcome.Unproven{Proposal: "p2", Agent: "agent-b", Tool: "save_response", Status: tooloutcome.Unknown, At: r.now})
	if calls := r.calls(t, ""); len(calls) != 2 {
		t.Fatalf("before the removal: %v", calls)
	}
	if err := r.agents.Archive(ctx, r.tnt, "agent-b", r.now); err != nil {
		t.Fatal(err)
	}
	calls := r.calls(t, "")
	if len(calls) != 1 || calls[0]["proposal"] != "p1" {
		t.Fatalf("after the removal: %v", calls)
	}
	if r.fake.answer != nil {
		t.Fatalf("an answer was recorded for the owner: %+v", r.fake.answer)
	}
}
