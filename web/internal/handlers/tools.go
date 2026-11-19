package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kiff/cloud/web/internal/apiclient"
	"github.com/kiff/cloud/web/internal/gatewayclient"
	"github.com/kiff/cloud/web/internal/pages"
	"github.com/kiff/cloud/web/internal/session"
)

// Tools: the owner connects remote MCP tools to the KIFF gateway from
// the dashboard. Every call goes to the gateway with the owner's own
// session bearer; the gateway checks it through apps/api and decides who
// may manage tools. The web holds no gateway key, and a tool credential
// passes through to the gateway without being stored or logged here.

// SetGateway enables /tools against the gateway at c. Nil leaves the
// pages saying tools cannot be connected from this deployment.
func (h *DashboardHandler) SetGateway(c *gatewayclient.Client) { h.gateway = c }

func (h *DashboardHandler) registerTools(mux *http.ServeMux) {
	mux.HandleFunc("GET /tools", h.toolsIndex)
	mux.HandleFunc("GET /tools/new", h.toolsNew)
	mux.HandleFunc("POST /tools/discover", h.toolsDiscover)
	mux.HandleFunc("POST /tools", h.toolsCreate)
	mux.HandleFunc("POST /tools/{name}/test", h.toolsTest)
	mux.HandleFunc("POST /tools/{name}/credential", h.toolsSetCredential)
	mux.HandleFunc("POST /tools/{name}/mode", h.toolsSetMode)
	mux.HandleFunc("POST /tools/{name}/remove", h.toolsRemove)
	mux.HandleFunc("POST /tools/{name}/settings", h.toolsSetSettings)
	mux.HandleFunc("GET /tools/{name}", h.toolDetail("overview"))
	mux.HandleFunc("GET /tools/{name}/settings", h.toolDetail("settings"))
	mux.HandleFunc("GET /tools/{name}/settings/{sheet}", h.toolSheet)
	mux.HandleFunc("GET /tools/{name}/card", h.toolCardForm)
	mux.HandleFunc("POST /tools/{name}/card", h.issueToolCard)
}

// gatewayMessage is the sentence to show for a gateway failure: the
// gateway's own, which is written for owners and has credentials
// redacted, or a plain one when it could not be reached.
func gatewayMessage(err error) string {
	var ge *gatewayclient.Error
	if errors.As(err, &ge) {
		return ge.Message
	}
	return gatewayclient.ErrUnavailable.Error()
}

// toolsBearer is cardSession for pages that need the gateway; ok false
// means the response is written.
func (h *DashboardHandler) toolsBearer(w http.ResponseWriter, r *http.Request) (session.Session, string, bool) {
	s, ok := SessionFrom(r.Context())
	if !ok {
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return session.Session{}, "", false
	}
	if h.gateway == nil {
		return s, "", true
	}
	bearer, err := h.bearer(r.Context(), s)
	if err != nil {
		h.logger.WarnContext(r.Context(), "tools: refresh bearer failed", slog.String("err", err.Error()))
		h.sessions.Clear(w)
		http.Redirect(w, r, "/sign-in", http.StatusFound)
		return session.Session{}, "", false
	}
	return s, bearer, true
}

func (h *DashboardHandler) toolsIndex(w http.ResponseWriter, r *http.Request) {
	s, bearer, ok := h.toolsBearer(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	flash, _ := h.sessions.ReadFlash(w, r)
	v := pages.ToolsView{Email: s.Email, Subject: s.Subject, DisplayName: s.DisplayName, Flash: convertFlash(flash)}
	if h.gateway == nil {
		v.NotConfigured = true
	} else if tools, err := h.gateway.List(r.Context(), bearer); err != nil {
		h.logger.WarnContext(r.Context(), "tools: list failed", slog.String("err", err.Error()))
		v.LoadError = "Your tools could not be loaded. This is not a statement that none are connected. " + gatewayMessage(err)
	} else {
		v.Tools = tools
		v.Filter = pages.NormalizeToolsFilter(r.URL.Query().Get("q"), r.URL.Query().Get("show"))
		// Each tool's Card count. Without the Cards, no tool is called
		// uncovered: the count reads unknown instead.
		var cards []apiclient.Mandate
		cardsOK := false
		if h.api != nil {
			if cs, err := h.api.ListMandatesBearer(r.Context(), bearer); err != nil {
				h.logger.WarnContext(r.Context(), "tools: list cards failed", slog.String("err", err.Error()))
				v.CardsError = "The Cards on these tools could not be read, so their counts are unknown."
			} else {
				cards, cardsOK = cs, true
			}
		}
		v.Groups, v.Chips = pages.GroupTools(tools, cards, cardsOK, v.Filter, time.Now())
	}
	_ = pages.Tools(v).Render(r.Context(), w)
}

func (h *DashboardHandler) toolsNew(w http.ResponseWriter, r *http.Request) {
	s, _, ok := h.toolsBearer(w, r)
	if !ok {
		return
	}
	// "Add a tool from this server" fills the address in; the
	// credential is never carried in a link.
	serverURL := strings.TrimSpace(r.URL.Query().Get("url"))
	if len(serverURL) > 2048 {
		serverURL = ""
	}
	h.renderToolsNew(w, r, http.StatusOK, pages.ToolsNewView{Email: s.Email, Subject: s.Subject, DisplayName: s.DisplayName, URL: serverURL})
}

// renderToolsNew writes either step. A page that can carry the
// credential in its forms is never stored by the browser or a proxy.
func (h *DashboardHandler) renderToolsNew(w http.ResponseWriter, r *http.Request, status int, v pages.ToolsNewView) {
	v.NotConfigured = h.gateway == nil
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	_ = pages.ToolsNew(v).Render(r.Context(), w)
}

const maxToolForm = 32 << 10

// toolsForm parses a /tools form within maxToolForm.
func toolsForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxToolForm)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return false
	}
	return true
}

// toolsDiscover is step 2: the server's tools, listed by the gateway
// with the credential. Nothing is saved yet.
func (h *DashboardHandler) toolsDiscover(w http.ResponseWriter, r *http.Request) {
	s, bearer, ok := h.toolsBearer(w, r)
	if !ok || !toolsForm(w, r) {
		return
	}
	v := pages.ToolsNewView{Email: s.Email, Subject: s.Subject, DisplayName: s.DisplayName,
		URL: strings.TrimSpace(r.PostFormValue("url"))}
	if h.gateway == nil {
		h.renderToolsNew(w, r, http.StatusServiceUnavailable, v)
		return
	}
	h.discoverInto(r.Context(), bearer, &v, r.PostFormValue("credential"))
	status := http.StatusOK
	if v.Error != "" {
		status = http.StatusBadRequest
	}
	h.renderToolsNew(w, r, status, v)
}

// discoverInto lists the server's tools into v; on failure v stays on
// step 1 with the gateway's sentence, and the credential is dropped.
func (h *DashboardHandler) discoverInto(ctx context.Context, bearer string, v *pages.ToolsNewView, credential string) {
	u, tools, err := h.gateway.Discover(ctx, bearer, v.URL, credential)
	switch {
	case err != nil:
		v.Error = gatewayMessage(err)
	case len(tools) == 0:
		v.Error = "The server answered, but lists no tools."
	default:
		v.URL, v.Discovered, v.Credential = u, tools, credential
	}
}

func (h *DashboardHandler) toolsCreate(w http.ResponseWriter, r *http.Request) {
	s, bearer, ok := h.toolsBearer(w, r)
	if !ok || !toolsForm(w, r) {
		return
	}
	n := gatewayclient.NewConnection{
		Name:              strings.TrimSpace(r.PostFormValue("name")),
		URL:               strings.TrimSpace(r.PostFormValue("url")),
		Tool:              r.PostFormValue("tool"),
		AmountArg:         r.PostFormValue("amount_argument"),
		IdempotencyArg:    r.PostFormValue("idempotency_argument"),
		ResourceArg:       r.PostFormValue("resource_argument"),
		OutcomeContract:   r.PostFormValue("outcome_contract"),
		OutcomeLookupTool: r.PostFormValue("outcome_lookup_tool"),
		AmountPath:        strings.TrimSpace(r.PostFormValue("amount_path")),
		ResourcePath:      strings.TrimSpace(r.PostFormValue("resource_path")),
		Credential:        r.PostFormValue("credential"),
		ReadOnly:          r.PostFormValue("read_only") == "true",
	}
	fixedTyped := strings.TrimSpace(r.PostFormValue("fixed_arguments"))
	var fixedErr string
	if fixedTyped != "" {
		// The gateway checks the arguments against the tool; here only that
		// it is a JSON object, so a typo comes back with the owner's text.
		var probe map[string]json.RawMessage
		if json.Unmarshal([]byte(fixedTyped), &probe) != nil {
			fixedErr = `"Always send" must be a JSON object, such as {"stripe_api_operation_id": "PostRefunds"}.`
		} else {
			n.FixedArgs = json.RawMessage(fixedTyped)
		}
	}
	unitChoice := r.PostFormValue("amount_unit")
	if n.AmountArg != "" || n.AmountPath != "" {
		// A unit says what the amount is; without an amount it means
		// nothing, and the gateway refuses it.
		if u, sc, ok := pages.ParseAmountUnit(unitChoice); ok {
			n.AmountUnit, n.AmountScale = u, sc
		} else {
			unitChoice = ""
		}
	}
	v := pages.ToolsNewView{Email: s.Email, Subject: s.Subject, DisplayName: s.DisplayName, URL: n.URL}
	if h.gateway == nil {
		h.renderToolsNew(w, r, http.StatusServiceUnavailable, v)
		return
	}
	var saved gatewayclient.Connection
	var err error
	if fixedErr == "" {
		saved, err = h.gateway.Create(r.Context(), bearer, n)
	}
	if fixedErr != "" || err != nil {
		// Back to step 2 with the owner's choices, the server's tools
		// listed again, and the gateway's reason on top.
		h.discoverInto(r.Context(), bearer, &v, n.Credential)
		if fixedErr != "" {
			v.Error = "The tool was not connected: " + fixedErr
		} else {
			v.Error = "The tool was not connected: " + gatewayMessage(err)
		}
		v.AmountPath, v.ResourcePath, v.FixedArgs = n.AmountPath, n.ResourcePath, fixedTyped
		v.Chosen, v.Name, v.AmountArg, v.IdempotencyArg, v.ReadOnly = n.Tool, n.Name, n.AmountArg, n.IdempotencyArg, n.ReadOnly
		v.AmountUnit = unitChoice
		v.OutcomeContract = n.OutcomeContract
		v.ResourceArg = n.ResourceArg
		v.OutcomeLookupTool = n.OutcomeLookupTool
		h.renderToolsNew(w, r, http.StatusBadRequest, v)
		return
	}
	h.logger.InfoContext(r.Context(), "tools: connected", slog.String("tool", saved.Name))
	_ = h.sessions.IssueFlash(w, session.Flash{Kind: session.FlashToolsChanged,
		Message: saved.Name + " is connected. Give an agent a Card for it, then point the agent at the KIFF gateway."})
	http.Redirect(w, r, "/tools/"+url.PathEscape(saved.Name), http.StatusSeeOther)
}

func (h *DashboardHandler) toolsTest(w http.ResponseWriter, r *http.Request) {
	h.toolsAct(w, r, func(ctx context.Context, bearer, name string) (session.Flash, error) {
		ok, msg, err := h.gateway.Test(ctx, bearer, name)
		if err != nil {
			return session.Flash{}, err
		}
		if !ok {
			return session.Flash{Kind: session.FlashError, Message: "The test of " + name + " failed: " + msg}, nil
		}
		return session.Flash{Kind: session.FlashToolsChanged, Message: msg}, nil
	})
}

func (h *DashboardHandler) toolsSetCredential(w http.ResponseWriter, r *http.Request) {
	h.toolsAct(w, r, func(ctx context.Context, bearer, name string) (session.Flash, error) {
		if err := h.gateway.SetCredential(ctx, bearer, name, r.PostFormValue("credential")); err != nil {
			return session.Flash{}, err
		}
		return session.Flash{Kind: session.FlashToolsChanged, Message: "The credential for " + name + " is replaced. KIFF reached the tool with it."}, nil
	})
}

// toolsSetMode changes who holds the tool's credential.
func (h *DashboardHandler) toolsSetMode(w http.ResponseWriter, r *http.Request) {
	h.toolsAct(w, r, func(ctx context.Context, bearer, name string) (session.Flash, error) {
		m := gatewayclient.ModeChange{Mode: r.PostFormValue("mode"), Audience: strings.TrimSpace(r.PostFormValue("audience")),
			URL: strings.TrimSpace(r.PostFormValue("url")), Credential: r.PostFormValue("credential")}
		if m.Mode == "verify" {
			m.Credential = ""
		}
		if err := h.gateway.SetMode(ctx, bearer, name, m); err != nil {
			return session.Flash{}, err
		}
		msg := map[string]string{
			"verify": name + " now checks KIFF's execution permit on each call. KIFF deleted the credential it held for it.",
			"relay":  name + " is now reached through your relay, which checks KIFF's execution permit and adds the tool's key. KIFF holds only the relay's transport credential.",
			"sealed": name + " is back to a credential KIFF stores, encrypted. KIFF reached the tool with it.",
		}[m.Mode]
		return session.Flash{Kind: session.FlashToolsChanged, Message: msg}, nil
	})
}

func (h *DashboardHandler) toolsRemove(w http.ResponseWriter, r *http.Request) {
	h.toolsAct(w, r, func(ctx context.Context, bearer, name string) (session.Flash, error) {
		if err := h.gateway.Remove(ctx, bearer, name); err != nil {
			return session.Flash{}, err
		}
		return session.Flash{Kind: session.FlashToolsChanged, Message: name + " is removed. Agents can no longer call it through KIFF."}, nil
	})
}

// toolsAct runs one action on a connected tool and returns with a
// sentence saying what happened: to the tool's own page when the form
// came from there (its "back" field), else to /tools.
func (h *DashboardHandler) toolsAct(w http.ResponseWriter, r *http.Request, act func(ctx context.Context, bearer, name string) (session.Flash, error)) {
	_, bearer, ok := h.toolsBearer(w, r)
	if !ok || !toolsForm(w, r) {
		return
	}
	name := r.PathValue("name")
	var f session.Flash
	if h.gateway == nil {
		f = session.Flash{Kind: session.FlashError, Message: "Tools cannot be managed from this deployment: it has no KIFF gateway configured."}
	} else if got, err := act(r.Context(), bearer, name); err != nil {
		h.logger.WarnContext(r.Context(), "tools: action failed", slog.String("tool", name), slog.String("err", gatewayMessage(err)))
		f = session.Flash{Kind: session.FlashError, Message: gatewayMessage(err)}
	} else {
		f = got
	}
	_ = h.sessions.IssueFlash(w, f)
	back := "/tools"
	if b := r.PostFormValue("back"); strings.HasPrefix(b, "/tools/"+url.PathEscape(name)) && !strings.Contains(b, "//") && !strings.HasSuffix(r.URL.Path, "/remove") {
		back = b
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}
