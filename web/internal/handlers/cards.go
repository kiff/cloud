package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/pages"
	"github.com/kiff/cloud/web/internal/session"
)

func (h *DashboardHandler) cards(w http.ResponseWriter, r *http.Request) {
	s, bearer, ok := h.cardSession(w, r)
	if !ok {
		return
	}
	flash, _ := h.sessions.ReadFlash(w, r)
	v := pages.CardsView{Email: s.Email, Subject: s.Subject, DisplayName: s.DisplayName, Flash: convertFlash(flash), Usage: map[string]pages.CardBalance{}}
	if h.api == nil {
		v.NotConfiguredMsg = "apps/api is not configured. Set KIFF_API_BASE_URL on the web binary to see your cards."
		_ = pages.Cards(v).Render(r.Context(), w)
		return
	}
	cards, err := h.api.ListMandatesBearer(r.Context(), bearer)
	if err != nil {
		v.LoadError = "Your cards could not be loaded. This is not a statement that none exist."
		_ = pages.Cards(v).Render(r.Context(), w)
		return
	}
	v.Holder = strings.TrimSpace(r.URL.Query().Get("holder"))
	v.View = r.URL.Query().Get("view")
	if v.View != "list" {
		v.View = "gallery"
	}
	v.Status = r.URL.Query().Get("status")
	if v.Status != "revoked" && v.Status != "archived" {
		v.Status = "active"
	}
	for _, m := range cards {
		if v.Holder != "" && !m.HeldBy(v.Holder) {
			continue
		}
		if !cardMatchesInventory(m, v.Status, time.Now()) {
			continue
		}
		v.Cards = append(v.Cards, m)
	}
	v.Usage = h.cardBalances(r.Context(), bearer, v.Cards)
	_ = pages.Cards(v).Render(r.Context(), w)
}

// cardBalances reads each card's balance, one statement call per card in
// parallel: fetched sequentially this was N round trips to the API
// before a page could send a single byte of HTML.
func (h *DashboardHandler) cardBalances(ctx context.Context, bearer string, cards []apiclient.Mandate) map[string]pages.CardBalance {
	out := make(map[string]pages.CardBalance, len(cards))
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, m := range cards {
		wg.Add(1)
		go func(m apiclient.Mandate) {
			defer wg.Done()
			st, err := h.api.MandateStatementBearer(ctx, bearer, m.ID)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				out[m.ID] = pages.CardBalance{Unread: true}
				return
			}
			out[m.ID] = pages.CardBalance{Limits: st.Limits}
		}(m)
	}
	wg.Wait()
	return out
}

func cardMatchesInventory(m apiclient.Mandate, status string, now time.Time) bool {
	if status == "archived" {
		return m.ArchivedAt != nil
	}
	if m.ArchivedAt != nil {
		return false
	}
	lapsed := m.RevokedAt != nil || (m.ValidUntil != nil && !now.Before(*m.ValidUntil))
	if status == "revoked" {
		return lapsed
	}
	return !lapsed
}

func (h *DashboardHandler) cardDetail(w http.ResponseWriter, r *http.Request) {
	h.renderCard(w, r, "overview")
}
func (h *DashboardHandler) cardDecisionsTab(w http.ResponseWriter, r *http.Request) {
	h.renderCard(w, r, "decisions")
}
func (h *DashboardHandler) cardStatement(w http.ResponseWriter, r *http.Request) {
	h.renderCard(w, r, "statement")
}
func (h *DashboardHandler) cardApproved(w http.ResponseWriter, r *http.Request) {
	h.renderCard(w, r, "approved")
}
func (h *DashboardHandler) cardChanges(w http.ResponseWriter, r *http.Request) {
	h.renderCard(w, r, "changes")
}
func (h *DashboardHandler) cardSettings(w http.ResponseWriter, r *http.Request) {
	h.renderCard(w, r, "settings")
}
func (h *DashboardHandler) cardApproversTab(w http.ResponseWriter, r *http.Request) {
	h.renderCard(w, r, "approvers")
}

func (h *DashboardHandler) renderCard(w http.ResponseWriter, r *http.Request, tab string) {
	s, bearer, ok := h.cardSession(w, r)
	if !ok {
		return
	}
	flash, _ := h.sessions.ReadFlash(w, r)
	v := pages.CardDetailView{Email: s.Email, Subject: s.Subject, DisplayName: s.DisplayName, Flash: convertFlash(flash), Tab: tab}
	if h.api == nil {
		v.LoadError = "apps/api is not configured."
		_ = pages.CardDetail(v).Render(r.Context(), w)
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	cards, err := h.api.ListMandatesBearer(r.Context(), bearer)
	if err != nil {
		v.LoadError = "This card could not be loaded."
		_ = pages.CardDetail(v).Render(r.Context(), w)
		return
	}
	for _, m := range cards {
		if m.ID == id {
			copy := m
			v.Card = &copy
			break
		}
	}
	if v.Card == nil {
		http.NotFound(w, r)
		return
	}
	if tab == "settings" {
		if me, e := h.api.GetMeBearer(r.Context(), bearer); e == nil && me.MemberRole == "admin" {
			v.WarningAdmin = true
			v.WarningConfig, err = h.api.CardWarningSettingsBearer(r.Context(), bearer, id)
			if err != nil {
				v.WarningError = "Budget warning settings could not be read."
			}
		}
	}
	// The Decisions tab reads the holder's recent decisions, the tenant's
	// keys and its kiff-guard runtimes for its connection status. Both are read beside the
	// statement rather than after it, so the page waits for the slowest
	// call, not their sum. The other tabs read neither.
	var decisions sync.WaitGroup
	var keys []apiclient.KeyListItem
	var keysErr error
	var runtimes []apiclient.GuardConnection
	var runtimesErr error
	// The Settings tab reads the tenant's keys too, beside the statement,
	// to offer the job's role to the key the agent already has.
	var holderKeys []apiclient.KeyListItem
	var holderKeysErr error
	var held []apiclient.CardException
	var heldErr error
	if tab == "settings" {
		decisions.Add(1)
		go func() {
			defer decisions.Done()
			holderKeys, holderKeysErr = h.api.ListKeysBearer(r.Context(), bearer)
		}()
	}
	if tab == "overview" {
		decisions.Add(1)
		go func() {
			defer decisions.Done()
			held, heldErr = h.api.ListCardExceptionsBearer(r.Context(), bearer, "held")
		}()
	}
	// The Approved once tab says what came of each approval, which only
	// the held action records.
	var answers []apiclient.CardException
	var answersErr error
	if tab == "approved" {
		decisions.Add(1)
		go func() {
			defer decisions.Done()
			answers, answersErr = h.api.ListCardExceptionsBearer(r.Context(), bearer, "")
		}()
	}
	if tab == "decisions" {
		decisions.Add(3)
		go func() {
			defer decisions.Done()
			runtimes, runtimesErr = h.api.ListGuardConnectionsBearer(r.Context(), bearer)
		}()
		go func() {
			defer decisions.Done()
			v.Decisions, v.DecisionsTruncated, v.DecisionsError = h.cardDecisions(r.Context(), bearer, *v.Card)
		}()
		go func() {
			defer decisions.Done()
			keys, keysErr = h.api.ListKeysBearer(r.Context(), bearer)
		}()
	}
	st, err := h.api.MandateStatementBearer(r.Context(), bearer, id)
	if err != nil {
		v.StatementError = "This card's balance, movements and changes could not be read."
	} else {
		v.Statement = st
		v.Balance = pages.CardBalance{Limits: st.Limits}
		if tab == "statement" {
			// Who added extra room, by email rather than sign-in id.
			// Best effort: unread members leave it unnamed.
			var members []apiclient.Member
			if pages.RoomGrantsByOthers(st, s.Subject) {
				if ms, err := h.api.MembersBearer(r.Context(), bearer); err == nil {
					members = ms
				}
			}
			pages.NameRoomGrants(st, s.Subject, s.Email, members)
		}
	}
	decisions.Wait()
	if tab == "approvers" {
		h.loadApprovers(r.Context(), bearer, &v)
	}
	if tab == "approved" {
		if answersErr != nil {
			h.logger.WarnContext(r.Context(), "cards: list answered holds failed", slog.String("card", id), slog.String("err", answersErr.Error()))
			v.AnswersUnreadable = true
		} else {
			v.Answers = make(map[string]apiclient.CardException, len(answers))
			for _, e := range answers {
				v.Answers[e.ID] = e
			}
		}
	}
	if tab == "overview" {
		if heldErr != nil {
			h.logger.WarnContext(r.Context(), "cards: list held exceptions failed", slog.String("card", id), slog.String("err", heldErr.Error()))
			v.HeldError = "Actions waiting for you on this card could not be read. Check Needs you before assuming there are none."
		} else {
			v.Held = pages.HeldForCard(held, id)
		}
	}
	if tab == "settings" {
		if holderKeysErr != nil {
			h.logger.WarnContext(r.Context(), "cards: list keys failed", slog.String("card", id), slog.String("err", holderKeysErr.Error()))
			v.HolderKeyUnknown = true
		} else {
			v.HolderKey = pages.HolderKeyFor(*v.Card, holderKeys)
		}
	}
	if tab == "decisions" {
		if keysErr != nil {
			h.logger.WarnContext(r.Context(), "cards: list keys failed", slog.String("card", id), slog.String("err", keysErr.Error()))
		}
		if runtimesErr != nil {
			h.logger.WarnContext(r.Context(), "cards: list guard runtimes failed", slog.String("card", id), slog.String("err", runtimesErr.Error()))
		}
		v.Connection = pages.NewCardConnection(v.Card.Subject, v.Decisions, v.DecisionsTruncated, keys, keysErr != nil, runtimes, runtimesErr != nil, h.clock())
		v.Decisions = firstDecisions(v.Decisions, cardDecisionsShown)
	}
	_ = pages.CardDetail(v).Render(r.Context(), w)
}

// cardDecisionsShown is how many of the holder's decisions the card
// lists; the rest are one link away in Activity. All that were read (up
// to 50) feed the connection status first.
const cardDecisionsShown = 10

func firstDecisions(ds []apiclient.ProposalDecision, n int) []apiclient.ProposalDecision {
	if len(ds) > n {
		return ds[:n]
	}
	return ds
}

// cardDecisions reads the card holder's recent decisions on the card's
// actions from the domain's evidence, every outcome included, each
// with its receipt when one has been signed. The evidence is filtered
// by actor on the server; the action filter is applied here, since an
// agent may act on other actions of the same domain under no card.
// cardDecisionsRead is the page of the holder's decisions the card reads
// before filtering to its actions.
const cardDecisionsRead = 50

// cardDecisions reads the holder's newest decisions in the card's domain
// and keeps those on the card's actions. truncated reports that the page
// read was full: the holder has older decisions that were not read, so
// counts drawn from it are lower bounds and an absence is not a fact.
func (h *DashboardHandler) cardDecisions(ctx context.Context, bearer string, m apiclient.Mandate) (rows []apiclient.ProposalDecision, truncated bool, errMsg string) {
	if m.Domain == "" || m.Subject == "" {
		return nil, false, ""
	}
	actions := map[string]bool{}
	for _, g := range m.Grants {
		actions[g.Action] = true
	}
	// One read per holder, filtered by the API to that agent, then merged
	// newest first. Reading the domain unfiltered and keeping the holders'
	// rows afterwards let other agents' traffic fill the page, and a
	// shared Card's tab came back empty though its decisions existed. A
	// Card has at most 20 holders.
	var out []apiclient.ProposalDecision
	for _, holder := range m.Holders() {
		ev, err := h.api.GetDomainEvidenceBearer(ctx, bearer, m.Domain, apiclient.ControlDecisionsOptions{
			Actor: holder,
			Limit: cardDecisionsRead,
		})
		if err != nil {
			h.logger.WarnContext(ctx, "cards: holder decisions failed",
				slog.String("card", m.ID), slog.String("holder", holder), slog.String("err", err.Error()))
			return nil, false, "This card's recent decisions could not be read. They are in Activity."
		}
		if ev.Decisions.Truncated || len(ev.Decisions.Rows) >= cardDecisionsRead {
			truncated = true
		}
		for _, d := range ev.Decisions.Rows {
			if d.ActorID != holder || (len(actions) > 0 && !actions[d.ActionName]) {
				continue
			}
			out = append(out, d)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if len(out) > cardDecisionsRead {
		out, truncated = out[:cardDecisionsRead], true
	}
	return out, truncated, ""
}

func (h *DashboardHandler) cardSession(w http.ResponseWriter, r *http.Request) (session.Session, string, bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return session.Session{}, "", false
	}
	if h.api == nil {
		return s, "", true
	}
	bearer, err := h.bearer(r.Context(), s)
	if err != nil {
		h.logger.WarnContext(r.Context(), "cards: refresh bearer failed", slog.String("err", err.Error()))
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return session.Session{}, "", false
	}
	return s, bearer, true
}
