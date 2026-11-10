// Package profile is a connection profile: what each of a
// vendor connection's operations does, what it acts on, where its amount
// is and how KIFF can tell it happened. Coach or the catalog writes the
// fields; KIFF checks them against the floor, renders the
// words the owner approves, and digests both, so the owner's signature
// covers exactly the profile and the words they saw.
package profile

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/kiff/cloud/api/internal/permit"
)

// Version names this shape. A profile carries it in "v".
const Version = "kiff.profile.v1"

// MaxBytes bounds a profile's JSON.
const MaxBytes = 256 << 10

// MaxOperations bounds how many operations one profile names.
const MaxOperations = 200

// Profile is one vendor connection's profile.
type Profile struct {
	V string `json:"v"`
	// Vendor is the name people read ("Stripe", "Holded").
	Vendor string `json:"vendor"`
	// Transport is how the vendor is reached: "mcp" (its MCP server) or
	// "rest" (its HTTP API, by key or OAuth).
	Transport string `json:"transport"`
	// Pin is the vendor version the profile was read from. An MCP server
	// can't be pinned: its pin is {"kind": "none"}.
	Pin Pin `json:"pin"`
	// Source is "catalog" (an entry merged by review) or "drafted" (Coach
	// drafted it; the owner approves it). Only a catalog profile may carry
	// no_effect (floor rule 1b).
	Source string `json:"source"`
	// CatalogEntry names the reviewed entry and its revision
	// ("stripe@3"); required for a catalog profile.
	CatalogEntry string `json:"catalog_entry,omitempty"`
	// TestProven: planque ran one test call per write with a credential
	// proven to be test-mode (floor rule 7). Without it, the first three
	// outcomes of each money write are the owner's to confirm (probation).
	TestProven bool        `json:"test_proven"`
	Operations []Operation `json:"operations"`
}

// Pin is how the profile is pinned to a vendor version.
type Pin struct {
	// Kind is "header" (a version header, such as Stripe-Version),
	// "base_path" ("/api/v2") or "none".
	Kind  string `json:"kind"`
	Value string `json:"value,omitempty"`
}

// Operation is one tool or endpoint the connection offers.
type Operation struct {
	// Tool is the operation's name at the gateway, as the agent calls it.
	Tool string `json:"tool"`
	// Title says what it does, in a few words ("Refund a payment").
	Title string `json:"title"`
	// Kind is "read" or "write".
	Kind string `json:"kind"`
	// Effect is what a write does; empty for a read.
	Effect string `json:"effect,omitempty"`
	// Shape is "single" or "batch": descriptive, never evidence.
	Shape    string    `json:"shape,omitempty"`
	Resource *Resource `json:"resource,omitempty"`
	Amount   *Amount   `json:"amount,omitempty"`
	// VerifyRead is the echo: a read after a 2xx write that gives the
	// amount the vendor used, or shows the write took effect.
	VerifyRead *VerifyRead `json:"verify_read,omitempty"`
	// Marker is the vendor field planque writes the operation id into.
	Marker *Marker `json:"marker,omitempty"`
	// StatusRead finds this operation without sending it again.
	StatusRead *StatusRead `json:"status_read,omitempty"`
	NoEffect   *NoEffect   `json:"no_effect,omitempty"`
	// RequiredArguments are arguments planque requires before sending.
	RequiredArguments []string `json:"required_arguments,omitempty"`
	// RecordURL is where the owner looks at the vendor's record of this
	// operation's resource ("https://dashboard.stripe.com/payments/{resource}"):
	// an https address at the vendor, with the resource's id path-escaped
	// into {resource} (RecordURL). Reviewed catalog only: a drafted profile
	// carries none, so no link reaches the owner that no reviewer saw. KIFF
	// shows it when it asks "Did this happen?" (kiff/tafs#498).
	RecordURL string `json:"record_url,omitempty"`
	// LabelRead is a read of the same profile that gives the resource's own
	// name (a customer's name, an invoice's number), for the words KIFF
	// asks with; it needs a resource.
	LabelRead *Read `json:"label_read,omitempty"`
	// Case makes a refund go through KIFF's governed path (thune): the
	// reads that give its facts, where the call names its charge.
	Case *Case `json:"case,omitempty"`
}

// Case is a money operation's case block (thune design v3, §4): what it
// is, where the call names its charge, and the reads of the same profile
// that give the facts KIFF decides on. A Card can't opt out of it.
type Case struct {
	// Kind is "refund", the one step one handles.
	Kind string `json:"kind"`
	// ChargeArg is the dotted path of the call's argument naming the
	// charge; the reads reference it as $args.<charge_arg>.
	ChargeArg string `json:"charge_arg"`
	// AccountRead gives the processor account the connection is on.
	AccountRead Read `json:"account_read"`
	// ChargeRead gives the charge's currency, captured amount and
	// dispute state.
	ChargeRead CaseChargeRead `json:"charge_read"`
	// RefundsRead lists the charge's refunds, everyone's.
	RefundsRead CaseRefundsRead `json:"refunds_read"`
	// RefundEvent, when set, is the app event that carries one refund's
	// state: the connection hands it to KIFF as well as to the taf.
	RefundEvent *CaseRefundEvent `json:"refund_event,omitempty"`
}

// CaseRefundEvent is the app event that carries a refund: its slug, where
// the refund is in the payload, and where the refund names its charge. The
// refund's id, amount, status and marker are read from it at the refunds
// read's paths.
type CaseRefundEvent struct {
	Event  string `json:"event"`
	Object string `json:"object"`
	Charge string `json:"charge"`
}

// CaseChargeRead reads a charge: its currency, its captured amount, and
// either a boolean that is true while a dispute is open, or a dispute
// status with its open values.
type CaseChargeRead struct {
	Tool          string            `json:"tool"`
	Args          map[string]string `json:"args,omitempty"`
	Currency      string            `json:"currency"`
	Captured      string            `json:"captured"`
	Disputed      string            `json:"disputed,omitempty"`
	DisputeStatus string            `json:"dispute_status,omitempty"`
	DisputeOpen   []string          `json:"dispute_open,omitempty"`
}

// CaseRefundsRead lists a charge's refunds: the list, and each refund's id,
// amount, status and the marker KIFF's calls write.
type CaseRefundsRead struct {
	Tool   string            `json:"tool"`
	Args   map[string]string `json:"args,omitempty"`
	List   string            `json:"list"`
	ID     string            `json:"id"`
	Amount string            `json:"amount"`
	Status string            `json:"status"`
	Marker string            `json:"marker"`
}

// CaseKindRefund is the one case kind step one handles.
const CaseKindRefund = "refund"

// RecordPlaceholder is what a record_url carries where the resource's id goes.
const RecordPlaceholder = "{resource}"

// Resource is what a call acts on, bound by the vendor's id.
type Resource struct {
	// Arg or Path is where the call carries the vendor's id.
	Arg  string `json:"arg,omitempty"`
	Path string `json:"path,omitempty"`
	// Noun is what it is, singular ("channel", "payment", "page").
	Noun string `json:"noun"`
	// Outside: for a sends operation, the recipient may be outside the
	// account's domain, a class of its own in the words.
	Outside bool `json:"outside,omitempty"`
}

// Amount is where a money operation's amount is and what it means.
type Amount struct {
	// From is "argument", "path" or "read".
	From string `json:"from"`
	Arg  string `json:"arg,omitempty"`
	Path string `json:"path,omitempty"`
	// Read is the read that says the amount, for From "read".
	Read *Read `json:"read,omitempty"`
	// Format is "integer_minor" (8000 is 80.00 at scale 2) or
	// "decimal_string" ("80.00"), which KIFF converts exactly and refuses
	// with more decimals than Scale.
	Format string `json:"format"`
	Scale  int    `json:"scale"`
	// Unit is the ISO 4217 currency, or UnitRead says where to read it.
	Unit     string `json:"unit,omitempty"`
	UnitRead *Read  `json:"unit_read,omitempty"`
}

// Read is one read operation of the same profile and the result path it
// takes a value from.
type Read struct {
	Tool string `json:"tool"`
	// Args maps the read's arguments to the call's: "$args.<name>" or
	// "$result.<path>"; anything else is sent as written.
	Args map[string]string `json:"args,omitempty"`
	Path string            `json:"path"`
}

// VerifyRead is the echo after a 2xx write.
type VerifyRead struct {
	Tool       string            `json:"tool"`
	Args       map[string]string `json:"args,omitempty"`
	AmountPath string            `json:"amount_path,omitempty"`
	DoneWhen   *Equals           `json:"done_when,omitempty"`
}

// Equals is a result path and the value that shows the write took effect.
type Equals struct {
	Path   string `json:"path"`
	Equals string `json:"equals"`
}

// Marker is the vendor's free field that keeps the operation id.
type Marker struct {
	Field string `json:"field"`
}

// StatusRead finds the operation by its marker, a list filter or a
// reference the vendor returns; or None, quoting the vendor's docs that
// say there is no way to check.
type StatusRead struct {
	Tool string            `json:"tool,omitempty"`
	Args map[string]string `json:"args,omitempty"`
	// Match is the result path compared with the operation id.
	Match string `json:"match,omitempty"`
	None  string `json:"none,omitempty"`
}

// NoEffect lists the vendor statuses that mean nothing happened, with the
// vendor's docs saying so.
type NoEffect struct {
	Statuses []int  `json:"statuses"`
	Docs     string `json:"docs"`
}

// Effects a write may have.
const (
	EffectDraft         = "none_until_approved"
	EffectSends         = "sends"
	EffectWrites        = "writes"
	EffectRecordsMoney  = "records_money"
	EffectMovesMoney    = "moves_money"
	maxScale            = 8
	maxNoEffectStatuses = 20
)

// Money reports whether the operation records or moves money.
func (o Operation) Money() bool {
	return o.Effect == EffectRecordsMoney || o.Effect == EffectMovesMoney
}

var (
	toolName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
	unitCode = regexp.MustCompile(`^[A-Z]{3}$`)
	// pathExpr is a path in a vendor's answer, with [n] for an array
	// index; argPath is one in the call's own arguments, which the
	// gateway follows by name only.
	pathExpr = regexp.MustCompile(`^[A-Za-z0-9_]+(\.[A-Za-z0-9_]+|\[[0-9]+\])*$`)
	argPath  = regexp.MustCompile(`^[A-Za-z0-9_]+(\.[A-Za-z0-9_]+){0,7}$`)
	argRef   = regexp.MustCompile(`^\$(args|result)\.[A-Za-z0-9_]+(\.[A-Za-z0-9_]+|\[[0-9]+\])*$`)
)

// Parse decodes a profile strictly and checks it against the floor.
func Parse(raw []byte) (Profile, error) {
	if len(raw) > MaxBytes {
		return Profile{}, fmt.Errorf("profile: larger than %d bytes", MaxBytes)
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var p Profile
	if err := dec.Decode(&p); err != nil {
		return Profile{}, fmt.Errorf("profile: %w", err)
	}
	if dec.More() {
		return Profile{}, errors.New("profile: trailing data after the profile")
	}
	return p, p.Validate()
}

// Validate checks the profile's shape and the floor rules a profile
// itself can break. Every message names the operation and field.
func (p Profile) Validate() error {
	if p.V != Version {
		return fmt.Errorf("profile: v must be %q", Version)
	}
	if strings.TrimSpace(p.Vendor) == "" || len(p.Vendor) > 80 {
		return errors.New("profile: vendor is the vendor's name, 1 to 80 characters")
	}
	switch p.Transport {
	case "mcp", "rest":
	default:
		return errors.New(`profile: transport is "mcp" or "rest"`)
	}
	switch p.Pin.Kind {
	case "none":
		if p.Pin.Value != "" {
			return errors.New("profile: pin none takes no value")
		}
	case "header", "base_path":
		if p.Transport == "mcp" {
			return errors.New("profile: an MCP server can't be pinned to a version; its pin is none")
		}
		if strings.TrimSpace(p.Pin.Value) == "" {
			return errors.New("profile: pin " + p.Pin.Kind + " needs its value")
		}
	default:
		return errors.New(`profile: pin.kind is "header", "base_path" or "none"`)
	}
	switch p.Source {
	case "catalog":
		if p.CatalogEntry == "" {
			return errors.New("profile: a catalog profile names its catalog_entry")
		}
	case "drafted":
		if p.CatalogEntry != "" {
			return errors.New("profile: a drafted profile has no catalog_entry")
		}
	default:
		return errors.New(`profile: source is "catalog" or "drafted"`)
	}
	if len(p.Operations) == 0 || len(p.Operations) > MaxOperations {
		return fmt.Errorf("profile: 1 to %d operations", MaxOperations)
	}
	byTool := map[string]Operation{}
	for _, o := range p.Operations {
		if !toolName.MatchString(o.Tool) {
			return fmt.Errorf("profile: operation tool %q: letters, digits, _ . - only", o.Tool)
		}
		if _, dup := byTool[o.Tool]; dup {
			return fmt.Errorf("profile: operation %s is named twice", o.Tool)
		}
		byTool[o.Tool] = o
	}
	for _, o := range p.Operations {
		if err := p.validateOp(o, byTool); err != nil {
			return fmt.Errorf("profile: operation %s: %w", o.Tool, err)
		}
	}
	return nil
}

func (p Profile) validateOp(o Operation, byTool map[string]Operation) error {
	if strings.TrimSpace(o.Title) == "" || len(o.Title) > 80 {
		return errors.New("title says what it does, 1 to 80 characters")
	}
	switch o.Kind {
	case "read":
		if o.Effect != "" || o.Amount != nil || o.VerifyRead != nil || o.Marker != nil || o.StatusRead != nil || o.NoEffect != nil {
			return errors.New("a read has no effect, amount, verify_read, marker, status_read or no_effect")
		}
	case "write":
		switch o.Effect {
		case EffectDraft, EffectSends, EffectWrites, EffectRecordsMoney, EffectMovesMoney:
		default:
			return errors.New("effect is none_until_approved, sends, writes, records_money or moves_money")
		}
	default:
		return errors.New(`kind is "read" or "write"`)
	}
	switch o.Shape {
	case "", "single", "batch":
	default:
		return errors.New(`shape is "single" or "batch"`)
	}
	if r := o.Resource; r != nil {
		if (r.Arg == "") == (r.Path == "") {
			return errors.New("resource names its arg or its path, one of them")
		}
		if r.Path != "" && !argPath.MatchString(r.Path) {
			return errors.New("resource.path is a dotted path in the call's arguments, names only")
		}
		if strings.TrimSpace(r.Noun) == "" || len(r.Noun) > 40 {
			return errors.New("resource.noun says what it is, 1 to 40 characters")
		}
		if r.Outside && o.Effect != EffectSends {
			return errors.New("resource.outside is only for a sends operation")
		}
	}
	if o.Money() != (o.Amount != nil) {
		if o.Money() {
			return errors.New("a money operation says where its amount is (amount)")
		}
		return errors.New("only records_money or moves_money has an amount")
	}
	if a := o.Amount; a != nil {
		if err := validateAmount(*a, byTool); err != nil {
			return err
		}
	}
	if v := o.VerifyRead; v != nil {
		if err := readOf(v.Tool, v.Args, byTool); err != nil {
			return fmt.Errorf("verify_read: %w", err)
		}
		if (v.AmountPath == "") == (v.DoneWhen == nil) {
			return errors.New("verify_read gives amount_path or done_when, one of them")
		}
		if v.AmountPath != "" && !o.Money() {
			return errors.New("verify_read.amount_path is for a money operation")
		}
		if v.AmountPath != "" && !pathExpr.MatchString(v.AmountPath) {
			return errors.New("verify_read.amount_path is a dotted path")
		}
		if d := v.DoneWhen; d != nil && (!pathExpr.MatchString(d.Path) || d.Equals == "") {
			return errors.New("verify_read.done_when is a dotted path and the value it equals")
		}
	}
	if m := o.Marker; m != nil && !pathExpr.MatchString(m.Field) {
		return errors.New("marker.field is a dotted path")
	}
	if s := o.StatusRead; s != nil {
		if s.None != "" {
			if s.Tool != "" || s.Match != "" || len(s.Args) > 0 {
				return errors.New("status_read none quotes the vendor's docs, and names no read")
			}
		} else {
			if err := readOf(s.Tool, s.Args, byTool); err != nil {
				return fmt.Errorf("status_read: %w", err)
			}
			if !pathExpr.MatchString(s.Match) {
				return errors.New("status_read.match is the result path compared with the operation id")
			}
		}
	}
	if n := o.NoEffect; n != nil {
		// Floor rule 1b: only a catalog entry merged by review, with the
		// vendor's docs, may say a status means nothing happened. Rule 3:
		// a 5xx, a 408 or a redirect is never one.
		if p.Source != "catalog" {
			return errors.New("no_effect comes only from a catalog entry merged by review (floor rule 1)")
		}
		if len(n.Statuses) == 0 || len(n.Statuses) > maxNoEffectStatuses || !strings.HasPrefix(n.Docs, "https://") {
			return errors.New("no_effect lists its statuses and the vendor's docs (an https link)")
		}
		for _, s := range n.Statuses {
			if s < 400 || s > 499 || s == 408 {
				return fmt.Errorf("no_effect: %d can't mean nothing happened (floor rule 3)", s)
			}
		}
	}
	for _, a := range o.RequiredArguments {
		if !toolName.MatchString(a) {
			return fmt.Errorf("required_arguments: %q isn't an argument name", a)
		}
	}
	if u := o.RecordURL; u != "" {
		if p.Source != "catalog" {
			return errors.New("record_url comes only from a catalog entry merged by review (reviewed catalog only)")
		}
		if err := checkRecordURL(u); err != nil {
			return fmt.Errorf("record_url: %w", err)
		}
	}
	if c := o.Case; c != nil {
		// A refund's money rules are reviewed code, never a model's draft.
		if p.Source != "catalog" {
			return errors.New("case: a case block comes only from a catalog entry merged by review (reviewed catalog only)")
		}
		if err := validateCase(o, *c, byTool); err != nil {
			return fmt.Errorf("case: %w", err)
		}
	}
	if l := o.LabelRead; l != nil {
		if o.Resource == nil {
			return errors.New("label_read names the resource's own name, so the operation needs a resource")
		}
		if err := readOf(l.Tool, l.Args, byTool); err != nil {
			return fmt.Errorf("label_read: %w", err)
		}
		if !pathExpr.MatchString(l.Path) {
			return errors.New("label_read.path is a dotted path")
		}
	}
	return nil
}

// checkRecordURL accepts an https address at a host, with no credentials in
// it and at most one {resource} placeholder, which is the only brace in it
// and sits in the path: never in the query or the fragment, where an id's
// "&" or "=" would add a parameter the vendor's page may act on.
func checkRecordURL(u string) error {
	if len(u) > 512 || !strings.HasPrefix(u, "https://") {
		return errors.New("an https address at the vendor, at most 512 characters")
	}
	if strings.Count(u, RecordPlaceholder) > 1 || strings.Count(u, "{") != strings.Count(u, RecordPlaceholder) || strings.Count(u, "}") != strings.Count(u, RecordPlaceholder) {
		return errors.New("{resource} is the only placeholder, at most once")
	}
	parsed, err := url.Parse(strings.ReplaceAll(u, RecordPlaceholder, "x"))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return errors.New("an https address at a host, with no credentials in it")
	}
	// Where the placeholder sits, by position: after the host, before any "?" or "#".
	if at := strings.Index(u, RecordPlaceholder); at >= 0 {
		host := len("https://") + len(parsed.Host)
		stop := strings.IndexAny(u, "?#")
		if at < host || (stop >= 0 && at > stop) {
			return errors.New("{resource} goes in the address's path, never its host, query or fragment")
		}
	}
	return nil
}

// RecordURL fills template with the resource's id, path-escaped, so an id
// holding "/", "?", "#" or "@" stays inside the vendor's host and path. An
// empty id, or "." or "..", which a browser reads as a step up the path,
// gives no link: the vendor's name alone is shown then.
func RecordURL(template, resource string) string {
	if !strings.Contains(template, RecordPlaceholder) {
		return template
	}
	if resource == "" || resource == "." || resource == ".." {
		return ""
	}
	return strings.ReplaceAll(template, RecordPlaceholder, url.PathEscape(resource))
}

func validateAmount(a Amount, byTool map[string]Operation) error {
	switch a.From {
	case "argument":
		if a.Arg == "" || a.Path != "" || a.Read != nil {
			return errors.New("an amount from an argument names only its arg")
		}
	case "path":
		if a.Path == "" || a.Arg != "" || a.Read != nil || !argPath.MatchString(a.Path) {
			return errors.New("an amount from a path names only its dotted path in the call's arguments, names only")
		}
	case "read":
		if a.Read == nil || a.Arg != "" || a.Path != "" {
			return errors.New("an amount from a read names only its read")
		}
		if err := readOf(a.Read.Tool, a.Read.Args, byTool); err != nil {
			return fmt.Errorf("amount.read: %w", err)
		}
		if !pathExpr.MatchString(a.Read.Path) {
			return errors.New("amount.read.path is a dotted path")
		}
	default:
		return errors.New(`amount.from is "argument", "path" or "read"`)
	}
	switch a.Format {
	case "integer_minor", "decimal_string":
	default:
		return errors.New(`amount.format is "integer_minor" or "decimal_string"`)
	}
	if a.Scale < 0 || a.Scale > maxScale {
		return fmt.Errorf("amount.scale is 0 to %d", maxScale)
	}
	if (a.Unit == "") == (a.UnitRead == nil) {
		return errors.New("amount gives its unit or its unit_read, one of them")
	}
	if a.Unit != "" && !unitCode.MatchString(a.Unit) {
		return errors.New("amount.unit is an ISO 4217 code, such as EUR")
	}
	if r := a.UnitRead; r != nil {
		if err := readOf(r.Tool, r.Args, byTool); err != nil {
			return fmt.Errorf("amount.unit_read: %w", err)
		}
		if !pathExpr.MatchString(r.Path) {
			return errors.New("amount.unit_read.path is a dotted path")
		}
	}
	return nil
}

// NarrowedParams is where a broad tool narrowed to one operation takes the
// operation's fields (Stripe's stripe_api_write through planque:
// parameters). The connection writes such a tool's marker inside them, so
// the record the operation makes carries it without this prefix: a case's
// refunds read may name the marker so, and nothing looser. planque allows
// that form only for a tool narrowed to one write.
const NarrowedParams = "parameters"

// validateCase checks a case block: a refund that moves money in a fixed
// currency, whose marker the case's refunds read finds, with reads of the
// same profile whose only reference is the call's charge.
func validateCase(o Operation, c Case, byTool map[string]Operation) error {
	if c.Kind != CaseKindRefund {
		return errors.New(`kind is "refund"`)
	}
	if o.Effect != EffectMovesMoney {
		return errors.New("a case is for an operation that moves money")
	}
	if o.Marker == nil {
		return errors.New("a case needs the operation's marker, to find KIFF's own refunds")
	}
	// KIFF compares the refund's currency with the charge's only when the
	// tool has a fixed unit: a case without one would be counted unchecked.
	if o.Amount == nil || o.Amount.Unit == "" {
		return errors.New("a case needs the operation's fixed amount.unit, so a refund is never counted in another currency")
	}
	if !argPath.MatchString(c.ChargeArg) {
		return errors.New("charge_arg is a dotted path in the call's arguments, names only")
	}
	charge := "$args." + c.ChargeArg
	reads := []struct {
		name string
		tool string
		args map[string]string
	}{
		{"account_read", c.AccountRead.Tool, c.AccountRead.Args},
		{"charge_read", c.ChargeRead.Tool, c.ChargeRead.Args},
		{"refunds_read", c.RefundsRead.Tool, c.RefundsRead.Args},
	}
	for _, r := range reads {
		if err := readOf(r.tool, r.args, byTool); err != nil {
			return fmt.Errorf("%s: %w", r.name, err)
		}
		for k, v := range r.args {
			// The watch reads a charge with no call in hand: the charge is
			// the only thing a case read may take from one.
			if strings.HasPrefix(v, "$") && v != charge {
				return fmt.Errorf("%s: argument %s may reference only %s", r.name, k, charge)
			}
		}
	}
	paths := map[string]string{
		"account_read.path":    c.AccountRead.Path,
		"charge_read.currency": c.ChargeRead.Currency,
		"charge_read.captured": c.ChargeRead.Captured,
		"refunds_read.list":    c.RefundsRead.List,
		"refunds_read.id":      c.RefundsRead.ID,
		"refunds_read.amount":  c.RefundsRead.Amount,
		"refunds_read.status":  c.RefundsRead.Status,
		"refunds_read.marker":  c.RefundsRead.Marker,
	}
	if e := c.RefundEvent; e != nil {
		if !toolName.MatchString(e.Event) {
			return errors.New("refund_event.event is the app's event slug")
		}
		paths["refund_event.object"], paths["refund_event.charge"] = e.Object, e.Charge
	}
	names := make([]string, 0, len(paths))
	for k := range paths {
		names = append(names, k)
	}
	slices.Sort(names)
	for _, k := range names {
		if !pathExpr.MatchString(paths[k]) {
			return fmt.Errorf("%s is a dotted path", k)
		}
	}
	if c.RefundsRead.Marker != o.Marker.Field && o.Marker.Field != NarrowedParams+"."+c.RefundsRead.Marker {
		return fmt.Errorf("refunds_read.marker is the operation's marker, %s, or, for a tool narrowed to one operation, the marker inside its %s", o.Marker.Field, NarrowedParams)
	}
	switch {
	case c.ChargeRead.Disputed != "" && c.ChargeRead.DisputeStatus != "":
		return errors.New("charge_read gives disputed or dispute_status, not both")
	case c.ChargeRead.Disputed != "":
		if !pathExpr.MatchString(c.ChargeRead.Disputed) || len(c.ChargeRead.DisputeOpen) > 0 {
			return errors.New("charge_read.disputed is a dotted path to a boolean")
		}
	case c.ChargeRead.DisputeStatus != "":
		if !pathExpr.MatchString(c.ChargeRead.DisputeStatus) || len(c.ChargeRead.DisputeOpen) == 0 {
			return errors.New("charge_read.dispute_status is a dotted path, with the dispute_open values")
		}
	default:
		// A refund on a disputed charge can pay twice: KIFF must see it.
		return errors.New("charge_read says where a dispute shows: disputed or dispute_status")
	}
	return nil
}

// readOf checks that tool is a read of the same profile, and that its
// argument references are well formed.
func readOf(tool string, args map[string]string, byTool map[string]Operation) error {
	o, ok := byTool[tool]
	if !ok {
		return fmt.Errorf("%q isn't an operation of this profile", tool)
	}
	if o.Kind != "read" {
		return fmt.Errorf("%s is a write; only a read can check", tool)
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if strings.HasPrefix(args[k], "$") && !argRef.MatchString(args[k]) {
			return fmt.Errorf("argument %s: a reference is $args.<name> or $result.<path>", k)
		}
	}
	return nil
}

// Digest is the hex SHA-256 of the profile's RFC 8785 (JCS) form: the
// profile_sha256 the owner's approval carries.
func Digest(raw []byte) (string, error) {
	return permit.ArgsSHA256(raw)
}
