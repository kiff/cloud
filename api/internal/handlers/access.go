package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/kiff/cloud/api/internal/access"
	"github.com/kiff/cloud/api/internal/agents"
	"github.com/kiff/cloud/api/internal/auth"
	"github.com/kiff/cloud/api/internal/membership"
)

// Audit actions for groups and grants, in the membership trail.
const (
	AuditGroupCreated   = "group.created"
	AuditGroupRenamed   = "group.renamed"
	AuditGroupDeleted   = "group.deleted"
	AuditGroupMemberSet = "group.member_set"
	AuditGroupMemberOut = "group.member_removed"
	AuditGrantAdded     = "grant.added"
	AuditGrantRemoved   = "grant.removed"
)

// AccessHandler serves groups, the sharing of agents with groups and
// people, and the permission answer: what a subject may do with
// an agent. KIFF is the source of truth for it; the runtimes that run
// agents ask, and the tafs app administers it.
//
//	GET    /v1/groups                          the account's groups, with their people
//	POST   /v1/groups                          {name}: members.manage
//	PATCH  /v1/groups/{id}                     {name}: members.manage
//	DELETE /v1/groups/{id}                     members.manage; its grants go with it
//	PUT    /v1/groups/{id}/members/{subject}   {role: manager|member}
//	DELETE /v1/groups/{id}/members/{subject}
//	GET    /v1/agents/{id}/grants              who the agent is shared with
//	POST   /v1/agents/{id}/grants              {group} or {subject}: cards.manage
//	DELETE /v1/agents/{id}/grants?group=|subject=  cards.manage
//	POST   /v1/agents/{id}/access              {subject}: the permission answer
//	GET    /v1/me/access                       the caller's answer for every agent
//	POST   /v1/me/access                       {subject}: an agent's key asks about its own agent
//
// A group's managers add and remove its members, among the account's
// people; naming or removing a manager takes members.manage.
type AccessHandler struct {
	store   access.Store
	members membership.Store
	agents  agents.Store
	// keys tell the account's Setup (Coach) by its key's setup_reader
	// role: Setup is for the account's admins by default. Nil: none is.
	keys auth.APIKeyStore
	// emails name a person in a share's words; nil: by their subject.
	emails EmailLookup
	now    func() time.Time
	logger *slog.Logger
}

// NewAccessHandler wires the handler.
func NewAccessHandler(store access.Store, members membership.Store, agentStore agents.Store, logger *slog.Logger) *AccessHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &AccessHandler{store: store, members: members, agents: agentStore, now: time.Now, logger: logger}
}

// WithEmails names a person in a share's words by their email; without
// it, by their subject.
func (h *AccessHandler) WithEmails(e EmailLookup) *AccessHandler {
	h.emails = e
	return h
}

// WithKeys lets the handler tell the account's Setup (Coach) by its key.
func (h *AccessHandler) WithKeys(k auth.APIKeyStore) *AccessHandler {
	h.keys = k
	return h
}

// setupAgents are the account's agents a live setup_reader key is bound
// to: its Setup.
func (h *AccessHandler) setupAgents(ctx context.Context, tenantID string) (map[string]bool, error) {
	out := map[string]bool{}
	if h.keys == nil {
		return out, nil
	}
	keys, err := h.keys.ListActive(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	for _, k := range keys {
		if k.AgentID == "" {
			continue
		}
		for _, role := range k.Roles {
			if role == auth.RoleSetupReader {
				out[k.AgentID] = true
			}
		}
	}
	return out, nil
}

// Register attaches the routes to the authenticated mux.
func (h *AccessHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/groups", h.listGroups)
	mux.HandleFunc("POST /v1/groups", h.createGroup)
	mux.HandleFunc("PATCH /v1/groups/{id}", h.renameGroup)
	mux.HandleFunc("DELETE /v1/groups/{id}", h.deleteGroup)
	mux.HandleFunc("PUT /v1/groups/{id}/members/{subject}", h.setMember)
	mux.HandleFunc("DELETE /v1/groups/{id}/members/{subject}", h.removeMember)
	mux.HandleFunc("GET /v1/agents/{id}/grants", h.listGrants)
	mux.HandleFunc("POST /v1/agents/{id}/grants", h.addGrant)
	mux.HandleFunc("DELETE /v1/agents/{id}/grants", h.removeGrant)
	mux.HandleFunc("POST /v1/agents/{id}/access", h.answer)
	mux.HandleFunc("GET /v1/me/access", h.myAccess)
	mux.HandleFunc("POST /v1/me/access", h.ownAgentAnswer)
}

type groupMemberView struct {
	Subject string           `json:"subject"`
	Role    access.GroupRole `json:"role"`
	// Can is what the person asking may do to this member, by the rules
	// setMember and removeMember apply.
	Can memberCan `json:"can"`
}

type groupView struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Everyone bool              `json:"everyone,omitempty"`
	Members  []groupMemberView `json:"members"`
	// Agents are the agents the group is granted.
	Agents []string `json:"agents"`
	// Can is what the person asking may do to the group, by the rules its
	// routes apply, so an app draws only those controls.
	Can groupCan `json:"can"`
}

// groupCan is what a person may do to a group: rename or delete it (members.
// manage), add a member (members.manage, or the group's manager), and set a
// member's role, which naming a manager takes (members.manage).
type groupCan struct {
	Rename  bool `json:"rename"`
	Delete  bool `json:"delete"`
	Add     bool `json:"add"`
	SetRole bool `json:"set_role"`
}

// memberCan is what a person may do to one member of a group: remove them,
// which a group's manager may for a member but not for another manager.
type memberCan struct {
	Remove bool `json:"remove"`
}

type grantView struct {
	Group   string `json:"group,omitempty"`
	Subject string `json:"subject,omitempty"`
	// What a share is called and means, in the owner's words, so no app
	// writes its own: name is the group's or the person's;
	// uses is what sharing it means, and stops what taking it back means.
	Name  string `json:"name,omitempty"`
	Uses  string `json:"uses,omitempty"`
	Stops string `json:"stops,omitempty"`
}

// fail writes a store error as the client reads it.
func (h *AccessHandler) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	switch {
	case errors.Is(err, access.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
	case errors.Is(err, access.ErrExists):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "group_exists", "detail": "a group of that name exists"})
	case errors.Is(err, access.ErrInvalidName):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_name", "detail": err.Error()})
	default:
		h.logger.ErrorContext(r.Context(), "access: "+what, slog.String("err", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": what + " failed"})
	}
}

// audit records a change in the account's membership trail. The change
// is made already; a trail that can't be written is logged, not undone.
func (h *AccessHandler) audit(r *http.Request, id auth.Identity, tenantID, action, target, oldV, newV string) {
	if h.members == nil {
		return
	}
	actor := actorOf(id, r)
	// A change the route policy verified signed says by what: the device key, an
	// approval (the approval key), or a passkey's assertion. From what the policy
	// verified, never from a header the client sets.
	if with, ok := auth.SignedWithFrom(r.Context()); ok {
		actor.AuthMethod += "+" + signedBy(with)
	}
	if err := h.members.Mutate(r.Context(), tenantID, func(tx membership.Tx) error {
		return tx.Audit(membership.AuditEvent{Actor: actor.Subject, Action: action, Target: target, OldValue: oldV, NewValue: newV,
			RequestID: actor.RequestID, AuthMethod: actor.AuthMethod})
	}); err != nil {
		h.logger.ErrorContext(r.Context(), "access: audit failed", slog.String("action", action), slog.String("err", err.Error()))
	}
}

// signedBy names how the route policy found a request signed, for the audit:
// "passkey" (a passkey's assertion), "critical" (the approval key) or
// "device" (the device key).
func signedBy(with auth.SignedWith) string {
	switch {
	case len(with.Passkey) > 0:
		return "passkey"
	case with.Critical():
		return auth.KeyCritical
	default:
		return auth.KeyDevice
	}
}

// liveMember is the subject's active membership on the account, or nil.
func (h *AccessHandler) liveMember(ctx context.Context, tenantID, subject string) (*membership.Member, error) {
	if h.members == nil {
		return nil, nil
	}
	m, err := h.members.Live(ctx, tenantID, subject)
	if errors.Is(err, membership.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !m.Active() {
		return nil, nil
	}
	return &m, nil
}

func (h *AccessHandler) listGroups(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := agentsIdentity(w, r)
	if !ok {
		return
	}
	admin := id.Can(membership.CapMembersManage)
	manages := map[string]bool{}
	if id.IsPerson() && !admin {
		mine, err := h.store.MembershipsOf(r.Context(), tenantID, id.Subject)
		if err != nil {
			h.fail(w, r, "list groups", err)
			return
		}
		for _, m := range mine {
			if m.Role == access.Manager {
				manages[m.Group] = true
			}
		}
	}
	groups, err := h.store.Groups(r.Context(), tenantID)
	if err != nil {
		h.fail(w, r, "list groups", err)
		return
	}
	grants, err := h.store.Grants(r.Context(), tenantID, "")
	if err != nil {
		h.fail(w, r, "list grants", err)
		return
	}
	out := make([]groupView, 0, len(groups))
	for _, g := range groups {
		ms, err := h.store.Members(r.Context(), tenantID, g.ID)
		if err != nil {
			h.fail(w, r, "list group members", err)
			return
		}
		manager := admin || manages[g.ID]
		v := groupView{ID: g.ID, Name: g.Name, Everyone: g.Everyone, Members: []groupMemberView{}, Agents: []string{},
			Can: groupCan{Rename: admin, Delete: admin, Add: manager, SetRole: admin}}
		for _, m := range ms {
			v.Members = append(v.Members, groupMemberView{Subject: m.Subject, Role: m.Role,
				Can: memberCan{Remove: admin || (manager && m.Role != access.Manager)}})
		}
		for _, gr := range grants {
			if gr.Group == g.ID {
				v.Agents = append(v.Agents, gr.Agent)
			}
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": out})
}

func (h *AccessHandler) createGroup(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := agentsIdentity(w, r)
	if !ok {
		return
	}
	if !id.Can(membership.CapMembersManage) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden", "detail": "making a group takes members.manage"})
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	name, err := access.NormalizeName(req.Name)
	if err != nil {
		h.fail(w, r, "create group", err)
		return
	}
	now := h.now()
	g := access.Group{TenantID: tenantID, ID: "grp_" + strings.ToLower(ulid.MustNew(ulid.Timestamp(now), ulid.DefaultEntropy()).String()),
		Name: name, CreatedAt: now, CreatedBy: id.Subject}
	if err := h.store.CreateGroup(r.Context(), g); err != nil {
		h.fail(w, r, "create group", err)
		return
	}
	h.audit(r, id, tenantID, AuditGroupCreated, g.ID, "", name)
	writeJSON(w, http.StatusCreated, groupView{ID: g.ID, Name: g.Name, Members: []groupMemberView{}, Agents: []string{}})
}

func (h *AccessHandler) renameGroup(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := agentsIdentity(w, r)
	if !ok {
		return
	}
	if !id.Can(membership.CapMembersManage) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden", "detail": "renaming a group takes members.manage"})
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	name, err := access.NormalizeName(req.Name)
	if err != nil {
		h.fail(w, r, "rename group", err)
		return
	}
	groupID := r.PathValue("id")
	was, err := h.store.Group(r.Context(), tenantID, groupID)
	if err == nil {
		err = h.store.RenameGroup(r.Context(), tenantID, groupID, name)
	}
	if err != nil {
		h.fail(w, r, "rename group", err)
		return
	}
	h.audit(r, id, tenantID, AuditGroupRenamed, groupID, was.Name, name)
	writeJSON(w, http.StatusOK, map[string]string{"id": groupID, "name": name})
}

func (h *AccessHandler) deleteGroup(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := agentsIdentity(w, r)
	if !ok {
		return
	}
	if !id.Can(membership.CapMembersManage) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden", "detail": "deleting a group takes members.manage"})
		return
	}
	groupID := r.PathValue("id")
	was, err := h.store.Group(r.Context(), tenantID, groupID)
	if err == nil {
		err = h.store.DeleteGroup(r.Context(), tenantID, groupID)
	}
	if err != nil {
		h.fail(w, r, "delete group", err)
		return
	}
	h.audit(r, id, tenantID, AuditGroupDeleted, groupID, was.Name, "")
	w.WriteHeader(http.StatusNoContent)
}

// managesGroup reports whether the caller may change the group's
// members: members.manage, or a manager of that group.
func (h *AccessHandler) managesGroup(r *http.Request, id auth.Identity, tenantID, groupID string) (bool, error) {
	if id.Can(membership.CapMembersManage) {
		return true, nil
	}
	if !id.IsPerson() {
		return false, nil
	}
	mine, err := h.store.MembershipsOf(r.Context(), tenantID, id.Subject)
	if err != nil {
		return false, err
	}
	for _, m := range mine {
		if m.Group == groupID && m.Role == access.Manager {
			return true, nil
		}
	}
	return false, nil
}

func (h *AccessHandler) setMember(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := agentsIdentity(w, r)
	if !ok {
		return
	}
	var req struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	role, valid := access.ParseGroupRole(req.Role)
	if !valid {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_role", "detail": "role is manager or member"})
		return
	}
	groupID, subject := r.PathValue("id"), r.PathValue("subject")
	if _, err := h.store.Group(r.Context(), tenantID, groupID); err != nil {
		h.fail(w, r, "set group member", err)
		return
	}
	allowed, err := h.managesGroup(r, id, tenantID, groupID)
	if err != nil {
		h.fail(w, r, "set group member", err)
		return
	}
	// A group's manager adds members; naming a manager, or changing one's
	// role, takes members.manage.
	if !allowed || (!id.Can(membership.CapMembersManage) && (role == access.Manager || h.isManager(r, tenantID, groupID, subject))) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden",
			"detail": "a group's managers add and remove its members; naming a manager takes members.manage"})
		return
	}
	m, err := h.liveMember(r.Context(), tenantID, subject)
	if err != nil {
		h.fail(w, r, "set group member", err)
		return
	}
	if m == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not_a_member", "detail": "only people on the account join its groups"})
		return
	}
	if err := h.store.SetMember(r.Context(), access.GroupMember{TenantID: tenantID, Group: groupID, Subject: subject, Role: role,
		AddedAt: h.now(), AddedBy: id.Subject}); err != nil {
		h.fail(w, r, "set group member", err)
		return
	}
	h.audit(r, id, tenantID, AuditGroupMemberSet, subject, groupID, string(role))
	writeJSON(w, http.StatusOK, groupMemberView{Subject: subject, Role: role})
}

// isManager reports whether subject manages the group.
func (h *AccessHandler) isManager(r *http.Request, tenantID, groupID, subject string) bool {
	ms, err := h.store.MembershipsOf(r.Context(), tenantID, subject)
	if err != nil {
		return true // can't tell: treat as a manager, which only refuses more
	}
	for _, m := range ms {
		if m.Group == groupID && m.Role == access.Manager {
			return true
		}
	}
	return false
}

func (h *AccessHandler) removeMember(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := agentsIdentity(w, r)
	if !ok {
		return
	}
	groupID, subject := r.PathValue("id"), r.PathValue("subject")
	if _, err := h.store.Group(r.Context(), tenantID, groupID); err != nil {
		h.fail(w, r, "remove group member", err)
		return
	}
	allowed, err := h.managesGroup(r, id, tenantID, groupID)
	if err != nil {
		h.fail(w, r, "remove group member", err)
		return
	}
	if !allowed || (!id.Can(membership.CapMembersManage) && h.isManager(r, tenantID, groupID, subject)) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden",
			"detail": "a group's managers add and remove its members; removing a manager takes members.manage"})
		return
	}
	if err := h.store.RemoveMember(r.Context(), tenantID, groupID, subject); err != nil {
		h.fail(w, r, "remove group member", err)
		return
	}
	h.audit(r, id, tenantID, AuditGroupMemberOut, subject, groupID, "")
	w.WriteHeader(http.StatusNoContent)
}

// liveAgent reports whether the account has the agent, not archived.
// liveRecord is the account's agent by id, unless archived; nil when it
// has none. With no agent store wired, every id is live, as in liveAgent,
// with no creator known.
func (h *AccessHandler) liveRecord(ctx context.Context, tenantID, agentID string) (*agents.Agent, error) {
	if h.agents == nil {
		return &agents.Agent{TenantID: tenantID, ID: agentID}, nil
	}
	list, err := h.agents.List(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	for _, a := range list {
		if a.ID == agentID && a.ArchivedAt == nil {
			return &a, nil
		}
	}
	return nil, nil
}

func (h *AccessHandler) liveAgent(ctx context.Context, tenantID, agentID string) (bool, error) {
	if h.agents == nil {
		return true, nil
	}
	list, err := h.agents.List(ctx, tenantID)
	if err != nil {
		return false, err
	}
	for _, a := range list {
		if a.ID == agentID {
			return a.ArchivedAt == nil, nil
		}
	}
	return false, nil
}

func (h *AccessHandler) listGrants(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := agentsIdentity(w, r)
	if !ok {
		return
	}
	// Who uses an agent is for whoever manages it: the people who may change
	// its sharing read it, and a person who only uses it doesn't.
	if !id.Can(membership.CapCardsManage) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden", "detail": "who an agent is shared with is for whoever manages it"})
		return
	}
	if !h.mayChange(w, r, id, tenantID, r.PathValue("id")) {
		return
	}
	grants, err := h.store.Grants(r.Context(), tenantID, r.PathValue("id"))
	if err != nil {
		h.fail(w, r, "list grants", err)
		return
	}
	agent := r.PathValue("id")
	taf := h.agentName(r.Context(), tenantID, agent)
	// Each person's email is looked up once for the whole read, however many
	// shares and groups name them: Clerk is rate-limited.
	ctx := withEmailCache(r.Context())
	out := make([]grantView, 0, len(grants))
	names := make([]string, 0, len(grants))
	for _, g := range grants {
		v := grantView{Group: g.Group, Subject: g.Subject}
		if g.Group != "" {
			v.Name, v.Uses = h.groupUses(ctx, tenantID, g.Group, taf)
		} else {
			v.Name = h.personName(ctx, g.Subject)
			v.Uses = v.Name + " uses " + taf + "."
		}
		v.Stops = v.Name + " stops using " + taf + ". Their chats stay theirs."
		out = append(out, v)
	}
	// Groups before people, each by name, so the line reads the same each time.
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Group != "") != (out[j].Group != "") {
			return out[i].Group != ""
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	for _, v := range out {
		names = append(names, v.Name)
	}
	shared := "Not shared"
	if len(names) > 0 {
		shared = "Shared with " + joinNames(names)
	}
	writeJSON(w, http.StatusOK, map[string]any{"grants": out, "shared_with": shared,
		"own": "Each person gets their own chats and notes. No one else sees them in the app."})
}

// agentName is what people read an agent by: its record's name, else its id.
func (h *AccessHandler) agentName(ctx context.Context, tenantID, agent string) string {
	if h.agents != nil {
		if list, err := h.agents.List(ctx, tenantID); err == nil {
			for _, a := range list {
				if a.ID == agent && a.Name != "" {
					return a.Name
				}
			}
		}
	}
	return agent
}

// personName is a person as a share names them: their email, else their
// subject.
func (h *AccessHandler) personName(ctx context.Context, subject string) string {
	cache, _ := ctx.Value(emailCacheKey{}).(map[string]string)
	if name, ok := cache[subject]; ok {
		return name
	}
	name := subject
	if h.emails != nil {
		if e, err := h.emails.EmailForSubject(ctx, subject); err == nil && strings.TrimSpace(e) != "" {
			name = e
		}
	}
	if cache != nil {
		cache[subject] = name
	}
	return name
}

// emailCacheKey keeps a request's email lookups, by subject.
type emailCacheKey struct{}

// withEmailCache gives ctx a cache for personName, for one request.
func withEmailCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, emailCacheKey{}, map[string]string{})
}

// groupUses is a group's name and what sharing taf with it means: "Support
// uses Refunds 4: ana@example.com and ben@example.com."
func (h *AccessHandler) groupUses(ctx context.Context, tenantID, group, taf string) (name, uses string) {
	name = group
	if g, err := h.store.Group(ctx, tenantID, group); err == nil {
		name = g.Name
	}
	people := []string{}
	if ms, err := h.store.Members(ctx, tenantID, group); err == nil {
		for _, m := range ms {
			people = append(people, h.personName(ctx, m.Subject))
		}
	}
	if len(people) == 0 {
		return name, name + " uses " + taf + ": no one yet."
	}
	return name, name + " uses " + taf + ": " + joinNames(people) + "."
}

// joinNames lists names as people say them: "A", "A and B", "A, B and C".
func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// grantFrom is the grant a request names: {group} or {subject} in the
// body, or ?group= / ?subject= on a removal.
func grantFrom(tenantID, agentID, group, subject string) (access.Grant, bool) {
	group, subject = strings.TrimSpace(group), strings.TrimSpace(subject)
	if (group == "") == (subject == "") {
		return access.Grant{}, false
	}
	return access.Grant{TenantID: tenantID, Agent: agentID, Group: group, Subject: subject}, true
}

func (h *AccessHandler) addGrant(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := agentsIdentity(w, r)
	if !ok {
		return
	}
	if !id.Can(membership.CapCardsManage) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden", "detail": "sharing an agent takes cards.manage"})
		return
	}
	if !h.mayChange(w, r, id, tenantID, r.PathValue("id")) {
		return
	}
	var req grantView
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	agentID := r.PathValue("id")
	g, valid := grantFrom(tenantID, agentID, req.Group, req.Subject)
	if !valid {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant", "detail": "name a group or a subject, not both"})
		return
	}
	live, err := h.liveAgent(r.Context(), tenantID, agentID)
	if err != nil {
		h.fail(w, r, "add grant", err)
		return
	}
	if !live {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found", "detail": "no agent of the account by that id"})
		return
	}
	if g.Subject != "" {
		m, err := h.liveMember(r.Context(), tenantID, g.Subject)
		if err != nil {
			h.fail(w, r, "add grant", err)
			return
		}
		if m == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not_a_member", "detail": "an agent is shared with people on the account"})
			return
		}
	}
	g.CreatedAt, g.CreatedBy = h.now(), id.Subject
	if err := h.store.AddGrant(r.Context(), g); err != nil {
		h.fail(w, r, "add grant", err)
		return
	}
	h.audit(r, id, tenantID, AuditGrantAdded, agentID, "", grantTarget(g))
	writeJSON(w, http.StatusOK, grantView{Group: g.Group, Subject: g.Subject})
}

func (h *AccessHandler) removeGrant(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := agentsIdentity(w, r)
	if !ok {
		return
	}
	if !id.Can(membership.CapCardsManage) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden", "detail": "unsharing an agent takes cards.manage"})
		return
	}
	if !h.mayChange(w, r, id, tenantID, r.PathValue("id")) {
		return
	}
	agentID := r.PathValue("id")
	g, valid := grantFrom(tenantID, agentID, r.URL.Query().Get("group"), r.URL.Query().Get("subject"))
	if !valid {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant", "detail": "name a group or a subject, not both"})
		return
	}
	if err := h.store.RemoveGrant(r.Context(), g); err != nil {
		h.fail(w, r, "remove grant", err)
		return
	}
	h.audit(r, id, tenantID, AuditGrantRemoved, agentID, grantTarget(g), "")
	w.WriteHeader(http.StatusNoContent)
}

// RemoveAgentGrants removes every grant of an archived agent, each one
// recorded in the trail as an unshare is: a share never outlives its
// agent (KIFF has no unarchive). An error leaves the rest in place, and
// grants on an archived agent give no one its use anyway (answersFor).
func (h *AccessHandler) RemoveAgentGrants(r *http.Request, id auth.Identity, tenantID, agentID string) error {
	if h == nil || h.store == nil {
		return nil
	}
	grants, err := h.store.Grants(r.Context(), tenantID, agentID)
	if err != nil {
		return err
	}
	for _, g := range grants {
		err := h.store.RemoveGrant(r.Context(), g)
		if errors.Is(err, access.ErrNotFound) {
			// Already removed, by someone else just now: theirs is the trail's row.
			continue
		}
		if err != nil {
			return err
		}
		h.audit(r, id, tenantID, AuditGrantRemoved, agentID, grantTarget(g), "")
	}
	return nil
}

// mayChange answers 403 when an editor shares or unshares a taf they
// didn't make, and reports whether to go on.
func (h *AccessHandler) mayChange(w http.ResponseWriter, r *http.Request, id auth.Identity, tenantID, agentID string) bool {
	ok, err := mayChangeTafs(r.Context(), h.agents, id, tenantID, agentID)
	if err != nil {
		h.fail(w, r, "access", err)
		return false
	}
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden", "detail": editorsOwnTafs})
	}
	return ok
}

// grantTarget names a grant's group or person in the trail.
func grantTarget(g access.Grant) string {
	if g.Group != "" {
		return "group:" + g.Group
	}
	return "subject:" + g.Subject
}

// answerView is the permission answer as the runtimes read it.
type answerView struct {
	access.Answer
	CheckedAt time.Time `json:"checked_at"`
	// CardsManage is whether the subject may manage the account's Cards
	// now, from their membership: only for Coach's key (setup_reader),
	// which hands a taf over for them with no session of theirs. Every other
	// caller's answer leaves it out.
	CardsManage *bool `json:"cards_manage,omitempty"`
}

// Answer is what subject may do with agentID: the permission answer, as
// the routes give it, for KIFF's own checks (a run grant on decide).
func (h *AccessHandler) Answer(ctx context.Context, tenantID, agentID, subject string) (access.Answer, error) {
	m, err := h.liveMember(ctx, tenantID, subject)
	if err != nil || m == nil {
		return access.Decide(nil, nil, nil), err
	}
	groups, err := h.store.MembershipsOf(ctx, tenantID, subject)
	if err != nil {
		return access.Answer{}, err
	}
	grants, err := h.store.Grants(ctx, tenantID, agentID)
	if err != nil {
		return access.Answer{}, err
	}
	a := access.Decide(m, groups, grants)
	setup, err := h.setupAgents(ctx, tenantID)
	if err != nil {
		return access.Answer{}, err
	}
	if setup[agentID] {
		a = access.ForSetup(a, m)
	}
	rec, err := h.liveRecord(ctx, tenantID, agentID)
	if err != nil {
		return access.Answer{}, err
	}
	if rec != nil {
		a = access.ForTaf(a, m, rec.Kind == agents.KindTaf, rec.CreatedBy)
	}
	return a, nil
}

// answer serves POST /v1/agents/{id}/access: what the subject may do
// with the agent. The runtime that runs the agent asks it with the agent's
// own key, never another agent's, so one taf can't probe another's grants;
// an account key may ask about any; a person asks about themselves only. Someone not on the account gets all false, and
// an agent the account doesn't have is 404. Answered fresh each time.
func (h *AccessHandler) answer(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := agentsIdentity(w, r)
	if !ok {
		return
	}
	var req struct {
		Subject string `json:"subject"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil || strings.TrimSpace(req.Subject) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request", "detail": "name the subject"})
		return
	}
	subject := strings.TrimSpace(req.Subject)
	if id.IsPerson() && subject != id.Subject {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden", "detail": "a person asks about themselves"})
		return
	}
	agentID := r.PathValue("id")
	if id.AgentID != "" && id.AgentID != agentID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden", "detail": "an agent's key asks about its own agent"})
		return
	}
	live, err := h.liveAgent(r.Context(), tenantID, agentID)
	if err != nil {
		h.fail(w, r, "access", err)
		return
	}
	if !live {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found", "detail": "no agent of the account by that id"})
		return
	}
	a, err := h.Answer(r.Context(), tenantID, agentID, subject)
	if err != nil {
		h.fail(w, r, "access", err)
		return
	}
	writeJSON(w, http.StatusOK, answerView{Answer: a, CheckedAt: h.now().UTC()})
}

// ownAgentAnswer serves POST /v1/me/access: the permission answer for the
// agent the calling key is bound to, so a runtime that holds only an
// agent's key asks about that agent without knowing its id.
func (h *AccessHandler) ownAgentAnswer(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := agentsIdentity(w, r)
	if !ok {
		return
	}
	if !id.APIKey || id.AgentID == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden", "detail": "an agent's key asks about its own agent"})
		return
	}
	var req struct {
		Subject string `json:"subject"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil || strings.TrimSpace(req.Subject) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request", "detail": "name the subject"})
		return
	}
	live, err := h.liveAgent(r.Context(), tenantID, id.AgentID)
	if err != nil {
		h.fail(w, r, "access", err)
		return
	}
	if !live {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found", "detail": "the key's agent is archived, or the account has none by its id"})
		return
	}
	a, err := h.Answer(r.Context(), tenantID, id.AgentID, strings.TrimSpace(req.Subject))
	if err != nil {
		h.fail(w, r, "access", err)
		return
	}
	v := answerView{Answer: a, CheckedAt: h.now().UTC()}
	if id.HasRole(auth.RoleSetupReader) {
		m, err := h.liveMember(r.Context(), tenantID, strings.TrimSpace(req.Subject))
		if err != nil {
			h.fail(w, r, "access", err)
			return
		}
		can := m != nil && membership.Can(m.Role, membership.CapCardsManage)
		v.CardsManage = &can
	}
	writeJSON(w, http.StatusOK, v)
}

// myAccess serves GET /v1/me/access: the caller's answer for each of the
// account's agents, so an app lists only what KIFF says the person may
// use, oversee or manage.
// answersFor is subject's permission answer for every agent of the account
// that isn't archived: GET /v1/me/access's, and what a person reads of the
// account's record (OwnUse, the shared-tafs design).
func (h *AccessHandler) answersFor(ctx context.Context, tenantID, subject string) (map[string]access.Answer, error) {
	out := map[string]access.Answer{}
	m, err := h.liveMember(ctx, tenantID, subject)
	if err != nil {
		return nil, err
	}
	var list []agents.Agent
	if h.agents != nil {
		if list, err = h.agents.List(ctx, tenantID); err != nil {
			return nil, err
		}
	}
	groups, err := h.store.MembershipsOf(ctx, tenantID, subject)
	if err != nil {
		return nil, err
	}
	grants, err := h.store.Grants(ctx, tenantID, "")
	if err != nil {
		return nil, err
	}
	setup, err := h.setupAgents(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	byAgent := map[string][]access.Grant{}
	for _, g := range grants {
		byAgent[g.Agent] = append(byAgent[g.Agent], g)
	}
	for _, a := range list {
		if a.ArchivedAt != nil {
			continue
		}
		out[a.ID] = access.Decide(m, groups, byAgent[a.ID])
		if setup[a.ID] {
			out[a.ID] = access.ForSetup(out[a.ID], m)
		}
		out[a.ID] = access.ForTaf(out[a.ID], m, a.Kind == agents.KindTaf, a.CreatedBy)
	}
	// Coach's agent is answered even before it has a record: an app asks
	// for it by this answer, never by a name it keeps.
	for id := range setup {
		if _, ok := out[id]; !ok {
			out[id] = access.ForSetup(access.Decide(m, groups, byAgent[id]), m)
		}
	}
	return out, nil
}

func (h *AccessHandler) myAccess(w http.ResponseWriter, r *http.Request) {
	id, tenantID, ok := agentsIdentity(w, r)
	if !ok {
		return
	}
	if !id.IsPerson() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden", "detail": "a person's own access"})
		return
	}
	out, err := h.answersFor(r.Context(), tenantID, id.Subject)
	if err != nil {
		h.fail(w, r, "access", err)
		return
	}
	body := map[string]any{"agents": out, "checked_at": h.now().UTC()}
	// Which agent is Coach's (the account's setup agent), so no app keeps
	// its id. Best effort: none when it can't be read. An
	// account has one; were there several, the first by id is named.
	if setup, err := h.setupAgents(r.Context(), tenantID); err == nil && len(setup) > 0 {
		ids := make([]string, 0, len(setup))
		for a := range setup {
			ids = append(ids, a)
		}
		sort.Strings(ids)
		body["setup_agent"] = ids[0]
	}
	writeJSON(w, http.StatusOK, body)
}
