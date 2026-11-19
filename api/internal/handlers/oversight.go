package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kiff/cloud/api/internal/access"
	"github.com/kiff/cloud/api/internal/membership"
	"github.com/kiff/cloud/api/internal/permit"
)

// AuditOversightRead is a read of another person's work, in the
// account's audit trail.
const AuditOversightRead = "oversight.read"

// Oversight is the one way to read another person's work with an agent:
// KIFF checks the reader oversees the agent, records the read with its
// purpose, and only then signs a read grant, which the agent's runtime
// asks KIFF to resolve before serving anything. The person whose work it
// is sees every read.
//
//	POST /v1/oversight/reads            {agent, owner, session?, purpose, note?}: a read grant
//	POST /v1/me/read-grants/resolve     {grant}: the agent's key, for its own agent
//	GET  /v1/me/oversight-reads         who read my work, when and why
//	GET  /v1/oversight/reads?agent=     the account's reads: members.manage
//
// It is off on every account (owner, 26 Oct): no admin, manager or owner
// reads an employee's conversations. A read is refused with
// oversight_off, and a grant already given no longer resolves. It may only
// come back, separately, as a process for a legal dispute or a forensic
// investigation. The record of reads made before stays readable.
type Oversight struct {
	Grants *RunGrants
	// On lets reads through. Nothing sets it in production; tests do, to
	// keep the record-first rules checked for when a legal process exists.
	On bool
}

// writeOversightOff answers a read of another person's work while reading
// it is off.
func writeOversightOff(w http.ResponseWriter) {
	writeJSON(w, http.StatusForbidden, map[string]string{"error": "oversight_off",
		"detail": "no one reads another person's conversations on this account"})
}

// Register attaches the routes to the authenticated mux.
func (o *Oversight) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/oversight/reads", o.read)
	mux.HandleFunc("POST /v1/me/read-grants/resolve", o.resolve)
	mux.HandleFunc("GET /v1/me/oversight-reads", o.mine)
	mux.HandleFunc("GET /v1/oversight/reads", o.account)
}

// maxReadNote bounds the reader's note, in characters.
const maxReadNote = 280

type readView struct {
	ID      string    `json:"id"`
	Reader  string    `json:"reader"`
	Owner   string    `json:"owner"`
	Agent   string    `json:"agent"`
	Session string    `json:"session,omitempty"`
	Purpose string    `json:"purpose"`
	Note    string    `json:"note,omitempty"`
	At      time.Time `json:"at"`
}

func viewRead(r access.Read) readView {
	return readView{ID: r.ID, Reader: r.Reader, Owner: r.Owner, Agent: r.Agent, Session: r.Session, Purpose: r.Purpose, Note: r.Note, At: r.At.UTC()}
}

func (o *Oversight) read(w http.ResponseWriter, r *http.Request) {
	g := o.Grants
	id, tenantID, ok := agentsIdentity(w, r)
	if !ok {
		return
	}
	if !o.On {
		writeOversightOff(w)
		return
	}
	if !id.IsPerson() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden", "detail": "a person reads another person's work, as themselves"})
		return
	}
	var req struct {
		Agent   string `json:"agent"`
		Owner   string `json:"owner"`
		Session string `json:"session"`
		Purpose string `json:"purpose"`
		Note    string `json:"note"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request", "detail": "invalid json"})
		return
	}
	agent, owner, session, note := strings.TrimSpace(req.Agent), strings.TrimSpace(req.Owner), strings.TrimSpace(req.Session), strings.TrimSpace(req.Note)
	switch {
	case agent == "" || owner == "":
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request", "detail": "name the agent and whose work"})
		return
	case !slices.Contains(access.Purposes, req.Purpose):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_purpose", "detail": "purpose is follow_up, fix or concern"})
		return
	case utf8.RuneCountInString(note) > maxReadNote:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_note", "detail": "a note is at most 280 characters"})
		return
	case owner == id.Subject:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "own_work", "detail": "your own work needs no read grant"})
		return
	case len(session) > 200:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request", "detail": "session is an id"})
		return
	}
	live, err := g.Access.liveAgent(r.Context(), tenantID, agent)
	if err != nil {
		g.fail(w, r, err)
		return
	}
	if !live {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found", "detail": "no agent of the account by that id"})
		return
	}
	member, err := g.Access.liveMember(r.Context(), tenantID, owner)
	if err != nil {
		g.fail(w, r, err)
		return
	}
	if member == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found", "detail": "no one on the account by that id"})
		return
	}
	may, err := o.mayRead(r.Context(), tenantID, agent, id.Subject, owner)
	if err != nil {
		g.fail(w, r, err)
		return
	}
	if !may {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "no_access", "need": "oversee",
			"detail": "an admin reads anyone's work; a manager, only their group's"})
		return
	}
	// Recorded before anything can be read: no grant without its record.
	now := g.now()
	rec := access.Read{TenantID: tenantID, ID: "rd_" + permit.NewJTI(), Reader: id.Subject, Owner: owner, Agent: agent,
		Session: session, Purpose: req.Purpose, Note: note, At: now.UTC()}
	if err := g.Access.store.RecordRead(r.Context(), rec); err != nil {
		g.fail(w, r, err)
		return
	}
	g.Access.audit(r, id, tenantID, AuditOversightRead, owner, "", strings.Join(slices.DeleteFunc([]string{agent, session, req.Purpose},
		func(s string) bool { return s == "" }), " "))
	tok, rg, err := g.Key.SignReadGrant(g.Iss, permit.ReadGrant{Jti: rec.ID, Tenant: tenantID, Agent: agent,
		Reader: id.Subject, Owner: owner, Session: session, Purpose: req.Purpose}, now)
	if err != nil {
		g.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"grant": tok, "expires_at": time.Unix(rg.Exp, 0).UTC(), "read": viewRead(rec)})
}

// errNoOversight is a read grant whose reader may no longer read its
// owner's work, or whose owner left the account.
var errNoOversight = errors.New("read grant: the reader may no longer read this work")

// mayRead is whether reader may read owner's work with agent now
// (access.MayRead): an admin, or a manager of a group the agent is
// granted to that the owner is in. Someone no longer on the account has
// no work to read.
func (o *Oversight) mayRead(ctx context.Context, tenantID, agent, reader, owner string) (bool, error) {
	a := o.Grants.Access
	rm, err := a.liveMember(ctx, tenantID, reader)
	if err != nil || rm == nil {
		return false, err
	}
	om, err := a.liveMember(ctx, tenantID, owner)
	if err != nil || om == nil {
		return false, err
	}
	readerGroups, err := a.store.MembershipsOf(ctx, tenantID, reader)
	if err != nil {
		return false, err
	}
	ownerGroups, err := a.store.MembershipsOf(ctx, tenantID, owner)
	if err != nil {
		return false, err
	}
	grants, err := a.store.Grants(ctx, tenantID, agent)
	if err != nil {
		return false, err
	}
	return access.MayRead(rm, readerGroups, ownerGroups, grants), nil
}

// CheckRead verifies a read grant for agent of tenant, and that its
// reader still oversees the agent.
func (o *Oversight) CheckRead(r *http.Request, tenantID, agent, token string) (permit.ReadGrant, error) {
	g := o.Grants
	rg, err := permit.VerifyReadGrant(strings.TrimSpace(token), g.Published, g.now())
	if err != nil {
		return permit.ReadGrant{}, err
	}
	if rg.Tenant != tenantID || rg.Agent != agent {
		return permit.ReadGrant{}, permit.ErrInvalidToken
	}
	// Checked again as it's used: a manager taken out of the group, or an
	// owner who left, loses the read.
	may, err := o.mayRead(r.Context(), tenantID, agent, rg.Reader, rg.Owner)
	if err != nil {
		return permit.ReadGrant{}, err
	}
	if !may {
		return permit.ReadGrant{}, errNoOversight
	}
	return rg, nil
}

func (o *Oversight) resolve(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := agentsIdentity(w, r)
	if !ok {
		return
	}
	if !id.APIKey || id.AgentID == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden", "detail": "an agent's key resolves the read grants for its own agent"})
		return
	}
	if !o.On {
		writeOversightOff(w)
		return
	}
	var req struct {
		Grant string `json:"grant"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil || strings.TrimSpace(req.Grant) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request", "detail": "name the grant"})
		return
	}
	rg, err := o.CheckRead(r, tenantID, id.AgentID, req.Grant)
	switch {
	case errors.Is(err, permit.ErrInvalidToken):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "read_grant", "detail": "not a valid read grant for this agent"})
	case errors.Is(err, errNoOversight):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "no_access", "need": "oversee"})
	case err != nil:
		o.Grants.fail(w, r, err)
	default:
		// read is KIFF's record of this read, so a runtime logs the read it
		// serves under the same id.
		writeJSON(w, http.StatusOK, map[string]string{"read": rg.Jti, "reader": rg.Reader, "owner": rg.Owner, "session": rg.Session, "purpose": rg.Purpose})
	}
}

func (o *Oversight) mine(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := agentsIdentity(w, r)
	if !ok {
		return
	}
	if !id.IsPerson() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden", "detail": "a person's own reads"})
		return
	}
	o.list(w, r, tenantID, id.Subject, "")
}

func (o *Oversight) account(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := agentsIdentity(w, r)
	if !ok {
		return
	}
	if !id.Can(membership.CapMembersManage) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden", "detail": "the account's reads take members.manage"})
		return
	}
	o.list(w, r, tenantID, "", strings.TrimSpace(r.URL.Query().Get("agent")))
}

func (o *Oversight) list(w http.ResponseWriter, r *http.Request, tenantID, owner, agent string) {
	reads, err := o.Grants.Access.store.Reads(r.Context(), tenantID, owner, agent, 200)
	if err != nil {
		o.Grants.fail(w, r, err)
		return
	}
	out := make([]readView, 0, len(reads))
	for _, rd := range reads {
		out = append(out, viewRead(rd))
	}
	writeJSON(w, http.StatusOK, map[string]any{"reads": out})
}
