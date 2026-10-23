// Package mcp is the slice of the Model Context Protocol the gateway
// speaks over Streamable HTTP: a server for agents and a client for the
// remote MCP tools it forwards to.
//
// Both sides are dual-era. Modern revisions (2026-07-28) carry the
// protocol version and client identity on every request, in params._meta
// mirrored into MCP-Protocol-Version, Mcp-Method and Mcp-Name headers,
// with no handshake or session. Legacy revisions (2025-03-26 to
// 2025-11-25) open with initialize. The server serves either era on the
// same endpoint, statelessly; the client detects a tool server's era with
// server/discover, which has no side effects, before it ever sends a
// tools/call.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kiff/cloud/gateway/internal/requestlog"
)

// ErrUnavailable is what Auth returns when the key couldn't be checked
// (KIFF unreachable, or its store down): the request is answered 503 with
// Retry-After, never 401, so an agent doesn't take its key for a bad one.
var ErrUnavailable = errors.New("mcp: the key couldn't be checked")

// Protocol versions, newest first.
var (
	ModernVersions = []string{"2026-07-28"}
	LegacyVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26"}
)

func supportedVersions() []string { return append(slices.Clone(ModernVersions), LegacyVersions...) }

const (
	metaVersion      = "io.modelcontextprotocol/protocolVersion"
	metaClientInfo   = "io.modelcontextprotocol/clientInfo"
	metaCapabilities = "io.modelcontextprotocol/clientCapabilities"
	metaServerInfo   = "io.modelcontextprotocol/serverInfo"
)

// JSON-RPC and MCP error codes.
const (
	codeParse              = -32700
	codeInvalidRequest     = -32600
	codeMethodNotFound     = -32601
	codeInvalidParams      = -32602
	codeInternal           = -32603
	codeHeaderMismatch     = -32020
	codeUnsupportedVersion = -32022
)

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// RPCError is a JSON-RPC error.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("mcp: %d %s", e.Code, e.Message) }

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// Tool is a tool as listed to an agent.
type Tool struct {
	Name string `json:"name"`
	// Title is the name people see.
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
	// Execution says whether the tool may run as an MCP task.
	Execution *ToolExecution `json:"execution,omitempty"`
	// Annotations say what a call does. Claude runs a read-only tool
	// without asking and asks before every call to a destructive one.
	Annotations *ToolAnnotations `json:"annotations,omitempty"`
}

// ToolAnnotations are a tool's behavior hints. Both are always sent.
type ToolAnnotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    bool   `json:"readOnlyHint"`
	DestructiveHint bool   `json:"destructiveHint"`
}

// ToolExecution is a tool's execution hints.
type ToolExecution struct {
	// TaskSupport: "optional" lets a client run the call as a task.
	TaskSupport string `json:"taskSupport,omitempty"`
}

// TextResult is a tools/call result carrying one text message.
func TextResult(text string, isError bool) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"content": []map[string]string{{"type": "text", "text": text}},
		"isError": isError,
	})
	return b
}

// Handler serves an authenticated agent's requests.
type Handler interface {
	ListTools(ctx context.Context) ([]Tool, error)
	// CallTool returns a tools/call result. An error is a protocol
	// failure; a refused or held call is a result with isError set.
	CallTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error)
}

// Authenticator resolves a request to a Handler for its caller, or
// returns an error to answer 401.
type Authenticator func(r *http.Request) (Handler, error)

// Server is the Streamable HTTP endpoint.
type Server struct {
	Auth         Authenticator
	Name         string
	Version      string
	Instructions string
	// Tasks declares the tasks capability; the handler implements
	// TaskHandler.
	Tasks bool
	// ResourceMetadata is the URL of the server's OAuth protected
	// resource metadata (RFC 9728), named in the 401 challenge so a
	// client can find where to sign in. Empty: no OAuth.
	ResourceMetadata string
	// Experimental are the server's experimental capabilities, declared
	// in capabilities.experimental. Empty: none.
	Experimental map[string]any
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		// Neither era needs GET (no standalone stream) or DELETE (no
		// sessions) here.
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "this endpoint accepts POST only", http.StatusMethodNotAllowed)
		return
	}
	// DNS-rebinding guard: agents do not send Origin; a browser page does,
	// and has no business here.
	if r.Header.Get("Origin") != "" {
		writeJSON(w, http.StatusForbidden, response{JSONRPC: "2.0", Error: &RPCError{Code: codeInvalidRequest, Message: "browser requests are not accepted"}})
		return
	}
	h, err := s.Auth(r)
	if errors.Is(err, ErrUnavailable) {
		// KIFF couldn't say whether the key is valid: try again, the key
		// may be fine (a 401 would read as a revoked key).
		w.Header().Set("Retry-After", "5")
		http.Error(w, "KIFF couldn't check the key just now; try again shortly", http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		challenge := `Bearer realm="kiff-gateway"`
		if s.ResourceMetadata != "" {
			challenge += `, resource_metadata="` + s.ResourceMetadata + `"`
		}
		w.Header().Set("WWW-Authenticate", challenge)
		http.Error(w, "a valid agent key is required", http.StatusUnauthorized)
		return
	}
	var req request
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, response{JSONRPC: "2.0", Error: &RPCError{Code: codeParse, Message: "invalid JSON"}})
		return
	}
	requestlog.Operation(r.Context(), req.Method)
	var params struct {
		Name string         `json:"name"`
		Meta map[string]any `json:"_meta"`
	}
	_ = json.Unmarshal(req.Params, &params)
	bodyVersion, _ := params.Meta[metaVersion].(string)
	headerVersion := r.Header.Get("MCP-Protocol-Version")
	modern := bodyVersion != "" || slices.Contains(ModernVersions, headerVersion)

	if modern {
		if status, rpcErr := validateModern(r, req, params.Name, headerVersion, bodyVersion); rpcErr != nil {
			writeJSON(w, status, response{JSONRPC: "2.0", ID: req.ID, Error: rpcErr})
			return
		}
	}
	if len(req.ID) == 0 {
		// A notification: nothing to answer.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		writeJSON(w, http.StatusBadRequest, response{JSONRPC: "2.0", ID: req.ID, Error: &RPCError{Code: codeInvalidRequest, Message: "invalid request"}})
		return
	}
	ctx, caller := withCaller(r.Context(), callerOf(req.Params, modern))
	var result any
	var rpcErr *RPCError
	if modern {
		result, rpcErr = s.dispatchModern(ctx, h, req)
	} else {
		result, rpcErr = s.dispatchLegacy(ctx, h, req)
	}
	switch {
	case rpcErr == nil && req.Method == "tools/call" && caller.task != nil:
		// A client that asked for a task waits on it; it is never also
		// asked to open a link for the same call.
		res := map[string]any{"task": caller.task.wire()}
		if modern {
			res["resultType"] = "complete"
		}
		result = res
	case rpcErr == nil && req.Method == "tools/call" && caller.elicit != nil:
		// RequestURLElicitation only takes a modern URL-capable caller.
		result = inputRequired(*caller.elicit)
	}
	status := http.StatusOK
	if modern && rpcErr != nil && rpcErr.Code == codeMethodNotFound {
		status = http.StatusNotFound
	}
	writeJSON(w, status, response{JSONRPC: "2.0", ID: req.ID, Result: result, Error: rpcErr})
}

// validateModern applies the 2026-07-28 header rules: the headers must be
// present and match the body, which stays the source of truth.
func validateModern(r *http.Request, req request, name, headerVersion, bodyVersion string) (int, *RPCError) {
	mismatch := func(msg string) (int, *RPCError) {
		return http.StatusBadRequest, &RPCError{Code: codeHeaderMismatch, Message: "Header mismatch: " + msg}
	}
	if headerVersion == "" || bodyVersion == "" {
		return mismatch("MCP-Protocol-Version header and _meta " + metaVersion + " are both required")
	}
	if headerVersion != bodyVersion {
		return mismatch("MCP-Protocol-Version header does not match _meta")
	}
	if !slices.Contains(ModernVersions, bodyVersion) {
		return http.StatusBadRequest, &RPCError{Code: codeUnsupportedVersion, Message: "Unsupported protocol version",
			Data: map[string]any{"supported": supportedVersions(), "requested": bodyVersion}}
	}
	// Checked for notifications too: an intermediary routes on these
	// headers whether or not the message expects a response.
	if m := r.Header.Get("Mcp-Method"); m == "" || m != req.Method {
		return mismatch("Mcp-Method header is missing or does not match the method")
	}
	if req.Method == "tools/call" {
		hn, ok := decodeHeaderValue(r.Header.Get("Mcp-Name"))
		if !ok || hn == "" || hn != name {
			return mismatch("Mcp-Name header is missing or does not match params.name")
		}
	}
	return 0, nil
}

// decodeHeaderValue undoes the =?base64?…?= sentinel encoding.
func decodeHeaderValue(v string) (string, bool) {
	inner, ok := strings.CutPrefix(v, "=?base64?")
	if !ok {
		return v, true
	}
	inner, ok = strings.CutSuffix(inner, "?=")
	if !ok {
		return "", false
	}
	b, err := base64.StdEncoding.DecodeString(inner)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// encodeHeaderValue applies the sentinel encoding where a value is not a
// plain header-safe string.
func encodeHeaderValue(v string) string {
	safe := v != "" && strings.TrimSpace(v) == v && !(strings.HasPrefix(v, "=?base64?") && strings.HasSuffix(v, "?="))
	for i := 0; safe && i < len(v); i++ {
		if c := v[i]; c < 0x20 || c > 0x7e {
			safe = false
		}
	}
	if safe {
		return v
	}
	return "=?base64?" + base64.StdEncoding.EncodeToString([]byte(v)) + "?="
}

func (s *Server) capabilities() map[string]any {
	c := map[string]any{"tools": map[string]any{"listChanged": false}}
	if s.Tasks {
		c["tasks"] = tasksCapability()
	}
	if len(s.Experimental) > 0 {
		c["experimental"] = s.Experimental
	}
	return c
}

func (s *Server) dispatchModern(ctx context.Context, h Handler, req request) (any, *RPCError) {
	switch req.Method {
	case "server/discover":
		return map[string]any{
			"resultType":        "complete",
			"supportedVersions": supportedVersions(),
			"capabilities":      s.capabilities(),
			"_meta":             map[string]any{metaServerInfo: map[string]string{"name": s.Name, "version": s.Version}},
			"instructions":      s.Instructions,
			// Caching hints are required on discover and list results.
			// Discovery is the same for every caller.
			"ttlMs":      int64(time.Hour / time.Millisecond),
			"cacheScope": "public",
		}, nil
	case "tools/list", "tools/call":
		res, err := s.tools(ctx, h, req)
		if err != nil {
			return nil, err
		}
		res = withResultType(res, true)
		if req.Method == "tools/list" {
			// The list is the caller's account's tools, so it is never
			// shared across keys.
			res = withFields(res, map[string]any{"ttlMs": int64(time.Minute / time.Millisecond), "cacheScope": "private"})
		}
		return res, nil
	case "tasks/get", "tasks/result":
		res, err := taskRequest(ctx, h, req)
		if err != nil {
			return nil, err
		}
		return withResultType(res, true), nil
	default:
		return nil, &RPCError{Code: codeMethodNotFound, Message: "Method not found: " + req.Method}
	}
}

func (s *Server) dispatchLegacy(ctx context.Context, h Handler, req request) (any, *RPCError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := LegacyVersions[0]
		if slices.Contains(LegacyVersions, p.ProtocolVersion) {
			version = p.ProtocolVersion
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    s.capabilities(),
			"serverInfo":      map[string]string{"name": s.Name, "version": s.Version},
			"instructions":    s.Instructions,
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list", "tools/call":
		res, err := s.tools(ctx, h, req)
		if err != nil {
			return nil, err
		}
		return withResultType(res, false), nil
	case "tasks/get", "tasks/result":
		res, err := taskRequest(ctx, h, req)
		if err != nil {
			return nil, err
		}
		return withResultType(res, false), nil
	default:
		return nil, &RPCError{Code: codeMethodNotFound, Message: "Method not found: " + req.Method}
	}
}

func (s *Server) tools(ctx context.Context, h Handler, req request) (json.RawMessage, *RPCError) {
	if req.Method == "tools/list" {
		tools, err := h.ListTools(ctx)
		if err != nil {
			return nil, &RPCError{Code: codeInternal, Message: "the tools could not be listed"}
		}
		if tools == nil {
			tools = []Tool{}
		}
		b, _ := json.Marshal(map[string]any{"tools": tools})
		return b, nil
	}
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
		return nil, &RPCError{Code: codeInvalidParams, Message: "tools/call needs a tool name"}
	}
	out, err := h.CallTool(ctx, p.Name, p.Arguments)
	if err != nil {
		var rpc *RPCError
		if errors.As(err, &rpc) {
			return nil, rpc
		}
		return nil, &RPCError{Code: codeInternal, Message: "the call could not be handled"}
	}
	return out, nil
}

// withResultType marks a result complete for modern clients and strips
// the marker for legacy ones, whichever era the stored result came from.
func withResultType(raw json.RawMessage, modern bool) json.RawMessage {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return raw
	}
	if modern {
		m["resultType"] = "complete"
	} else {
		delete(m, "resultType")
	}
	b, _ := json.Marshal(m)
	return b
}

func withFields(raw json.RawMessage, fields map[string]any) json.RawMessage {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return raw
	}
	for k, v := range fields {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return b
}

// UnknownTool is the protocol error for a tool the agent cannot reach.
func UnknownTool(name string) error {
	return &RPCError{Code: codeInvalidParams, Message: "Unknown tool: " + name}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Client calls tools on remote MCP servers. Each server's era is probed
// once and cached per URL.
type Client struct {
	HTTP *http.Client
	// Version is reported as the client's version.
	Version string

	mu   sync.Mutex
	eras map[string]bool // url → modern
}

// CallError reports a failed upstream call and whether the tools/call
// request may have reached the tool. Sent false means it certainly did
// not.
type CallError struct {
	Sent bool
	Err  error
}

func (e *CallError) Error() string { return e.Err.Error() }
func (e *CallError) Unwrap() error { return e.Err }

func notSent(err error) error { return &CallError{Sent: false, Err: err} }

// sentUnlessDial: once a tools/call request is being sent, only a
// failure to connect proves it never left.
func sentUnlessDial(err error) error {
	var op *net.OpError
	return &CallError{Sent: !(errors.As(err, &op) && op.Op == "dial"), Err: err}
}

// CallTool calls tool on the server at url and returns its tools/call
// result. A JSON-RPC error from the server comes back as *RPCError, and a
// transport failure as *CallError.
func (c *Client) CallTool(ctx context.Context, url, bearer, tool string, args json.RawMessage) (json.RawMessage, error) {
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	modern, err := c.era(ctx, url, bearer)
	if err != nil {
		return nil, notSent(fmt.Errorf("discover: %w", err))
	}
	if modern {
		return c.callModern(ctx, url, bearer, tool, args)
	}
	return c.callLegacy(ctx, url, bearer, tool, args)
}

func (c *Client) hc() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) meta() map[string]any {
	v := c.Version
	if v == "" {
		v = "1"
	}
	return map[string]any{
		metaVersion:      ModernVersions[0],
		metaClientInfo:   map[string]string{"name": "kiff-gateway", "version": v},
		metaCapabilities: map[string]any{},
	}
}

// postModern sends one modern request with its mirrored headers.
func (c *Client) postModern(ctx context.Context, url, bearer string, id int, method, name string, params map[string]any) (*http.Response, error) {
	params["_meta"] = c.meta()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", ModernVersions[0])
	req.Header.Set("Mcp-Method", method)
	if name != "" {
		req.Header.Set("Mcp-Name", encodeHeaderValue(name))
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if method == "tools/call" {
		callHeaders(ctx, req)
	}
	return c.hc().Do(req)
}

// era probes url with server/discover. A result or a recognized modern
// error means a modern server; anything else a legacy one.
func (c *Client) era(ctx context.Context, url, bearer string) (bool, error) {
	c.mu.Lock()
	modern, known := c.eras[url]
	c.mu.Unlock()
	if known {
		return modern, nil
	}
	resp, err := c.postModern(ctx, url, bearer, 0, "server/discover", "", map[string]any{})
	if err != nil {
		return false, err
	}
	msg, readErr := readMessage(resp, "0")
	resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK && readErr == nil && msg.Error == nil:
		var r struct {
			SupportedVersions []string `json:"supportedVersions"`
		}
		_ = json.Unmarshal(msg.Result, &r)
		modern = slices.ContainsFunc(r.SupportedVersions, func(v string) bool { return slices.Contains(ModernVersions, v) })
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return false, fmt.Errorf("status %d", resp.StatusCode)
	case readErr == nil && msg.Error != nil && isModernError(resp.StatusCode, msg.Error):
		modern = true
		if msg.Error.Code == codeUnsupportedVersion {
			return false, errors.New("the tool server supports none of the gateway's modern protocol versions")
		}
	default:
		modern = false
	}
	c.mu.Lock()
	if c.eras == nil {
		c.eras = map[string]bool{}
	}
	c.eras[url] = modern
	c.mu.Unlock()
	return modern, nil
}

func isModernError(status int, e *RPCError) bool {
	switch e.Code {
	case codeHeaderMismatch, codeUnsupportedVersion:
		return status == http.StatusBadRequest
	case codeMethodNotFound:
		return status == http.StatusNotFound
	}
	return false
}

// callHeaderKey carries extra headers for the tools/call request only.
type callHeaderKey struct{}

// WithCallHeader adds a header to the tools/call request CallTool sends
// with ctx, and to no other request (discovery, initialize).
func WithCallHeader(ctx context.Context, name, value string) context.Context {
	h := http.Header{}
	if prev, ok := ctx.Value(callHeaderKey{}).(http.Header); ok {
		h = prev.Clone()
	}
	h.Set(name, value)
	return context.WithValue(ctx, callHeaderKey{}, h)
}

// CallHeader returns a header WithCallHeader put on ctx, or "".
func CallHeader(ctx context.Context, name string) string {
	if h, ok := ctx.Value(callHeaderKey{}).(http.Header); ok {
		return h.Get(name)
	}
	return ""
}

func callHeaders(ctx context.Context, req *http.Request) {
	if h, ok := ctx.Value(callHeaderKey{}).(http.Header); ok {
		for k, v := range h {
			req.Header[k] = v
		}
	}
}

func (c *Client) callModern(ctx context.Context, url, bearer, tool string, args json.RawMessage) (json.RawMessage, error) {
	resp, err := c.postModern(ctx, url, bearer, 1, "tools/call", tool, map[string]any{"name": tool, "arguments": args})
	if err != nil {
		return nil, sentUnlessDial(fmt.Errorf("tools/call: %w", err))
	}
	defer resp.Body.Close()
	msg, err := readMessage(resp, "1")
	if err != nil {
		return nil, &CallError{Sent: true, Err: fmt.Errorf("tools/call: %w", err)}
	}
	if msg.Error != nil {
		if isModernError(resp.StatusCode, msg.Error) {
			// Rejected at the transport, before the tool ran.
			return nil, notSent(msg.Error)
		}
		return nil, msg.Error
	}
	var r struct {
		ResultType string `json:"resultType"`
	}
	_ = json.Unmarshal(msg.Result, &r)
	if r.ResultType == "input_required" {
		return nil, &RPCError{Code: codeInternal, Message: "the tool asked for more input, which KIFF does not pass to agents yet"}
	}
	return msg.Result, nil
}

type message struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *RPCError       `json:"error"`
}

// readMessage reads the response with id want from a JSON or SSE body.
func readMessage(resp *http.Response, want string) (message, error) {
	var msg message
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&msg); err != nil {
			return message{}, fmt.Errorf("status %d: %w", resp.StatusCode, err)
		}
		return msg, nil
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	for sc.Scan() {
		line, ok := strings.CutPrefix(sc.Text(), "data:")
		if !ok {
			continue
		}
		msg = message{}
		if json.Unmarshal([]byte(strings.TrimSpace(line)), &msg) == nil && string(msg.ID) == want {
			return msg, nil
		}
	}
	if err := sc.Err(); err != nil {
		return message{}, err
	}
	return message{}, errors.New("stream ended without a response")
}

// callLegacy opens a session with initialize, then calls the tool.
func (c *Client) callLegacy(ctx context.Context, url, bearer, tool string, args json.RawMessage) (json.RawMessage, error) {
	sess := &session{hc: c.hc(), url: url, bearer: bearer}
	if _, err := sess.rpc(ctx, 1, "initialize", map[string]any{
		"protocolVersion": LegacyVersions[0],
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "kiff-gateway", "version": "1"},
	}); err != nil {
		return nil, notSent(fmt.Errorf("initialize: %w", err))
	}
	if err := sess.notify(ctx, "notifications/initialized"); err != nil {
		return nil, notSent(fmt.Errorf("initialized: %w", err))
	}
	sess.call = ctx
	res, err := sess.rpc(ctx, 2, "tools/call", map[string]any{"name": tool, "arguments": args})
	if err != nil {
		var rpc *RPCError
		if errors.As(err, &rpc) {
			return nil, rpc
		}
		return nil, sentUnlessDial(fmt.Errorf("tools/call: %w", err))
	}
	return res, nil
}

type session struct {
	hc      *http.Client
	url     string
	bearer  string
	id      string
	version string
	// call, once set, is the context whose call headers go on the
	// requests that follow (the tools/call).
	call context.Context
}

func (s *session) post(ctx context.Context, body any) (*http.Response, error) {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if s.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+s.bearer)
	}
	if s.id != "" {
		req.Header.Set("Mcp-Session-Id", s.id)
	}
	if s.version != "" {
		req.Header.Set("MCP-Protocol-Version", s.version)
	}
	if s.call != nil {
		callHeaders(s.call, req)
	}
	return s.hc.Do(req)
}

func (s *session) notify(ctx context.Context, method string) error {
	resp, err := s.post(ctx, map[string]any{"jsonrpc": "2.0", "method": method})
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

func (s *session) rpc(ctx context.Context, id int, method string, params any) (json.RawMessage, error) {
	resp, err := s.post(ctx, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		s.id = sid
	}
	msg, err := readMessage(resp, fmt.Sprint(id))
	if err != nil {
		return nil, err
	}
	if msg.Error != nil {
		return nil, msg.Error
	}
	if method == "initialize" {
		var r struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(msg.Result, &r)
		s.version = r.ProtocolVersion
	}
	return msg.Result, nil
}

// RemoteTool is a tool as a remote server lists it.
type RemoteTool struct {
	Name string `json:"name"`
	// Title is the name the vendor gives people, when it gives one.
	Title       string           `json:"title,omitempty"`
	Description string           `json:"description"`
	InputSchema json.RawMessage  `json:"inputSchema"`
	Annotations *ToolAnnotations `json:"annotations,omitempty"`
}

// maxListPages bounds how many pages of tools are read from one server.
const maxListPages = 20

// ListTools lists the tools of the server at url, following pages.
func (c *Client) ListTools(ctx context.Context, url, bearer string) ([]RemoteTool, error) {
	modern, err := c.era(ctx, url, bearer)
	if err != nil {
		return nil, fmt.Errorf("discover: %w", err)
	}
	var sess *session
	if !modern {
		sess = &session{hc: c.hc(), url: url, bearer: bearer}
		if _, err := sess.rpc(ctx, 1, "initialize", map[string]any{
			"protocolVersion": LegacyVersions[0], "capabilities": map[string]any{},
			"clientInfo": map[string]string{"name": "kiff-gateway", "version": "1"},
		}); err != nil {
			return nil, fmt.Errorf("initialize: %w", err)
		}
		if err := sess.notify(ctx, "notifications/initialized"); err != nil {
			return nil, fmt.Errorf("initialized: %w", err)
		}
	}
	var out []RemoteTool
	cursor := ""
	for page := 0; page < maxListPages; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var raw json.RawMessage
		if modern {
			resp, err := c.postModern(ctx, url, bearer, 10+page, "tools/list", "", params)
			if err != nil {
				return nil, err
			}
			msg, err := readMessage(resp, fmt.Sprint(10+page))
			resp.Body.Close()
			if err != nil {
				return nil, err
			}
			if msg.Error != nil {
				return nil, msg.Error
			}
			raw = msg.Result
		} else if raw, err = sess.rpc(ctx, 10+page, "tools/list", params); err != nil {
			return nil, err
		}
		var r struct {
			Tools      []RemoteTool `json:"tools"`
			NextCursor string       `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		out = append(out, r.Tools...)
		if r.NextCursor == "" {
			return out, nil
		}
		cursor = r.NextCursor
	}
	return nil, errors.New("the tool server lists more pages of tools than KIFF reads")
}
