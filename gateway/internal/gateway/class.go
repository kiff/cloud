package gateway

import (
	"embed"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"unicode"
)

// A call's class (K4): what the worst call of a
// tool can do. KIFF computes it itself, beside the read_only planque sends
// (K1), and never lower than planque's word: a tool planque doesn't call
// read-only is never a read here.
type Class string

const (
	ClassRead         Class = "read"
	ClassWrite        Class = "write"
	ClassIrreversible Class = "irreversible" // finalise, send or share: can't be undone
	ClassDelete       Class = "delete"
	ClassMoney        Class = "money"
)

// classRank orders the classes from least to most: a tool takes the worst
// any rule gives it.
var classRank = map[Class]int{ClassRead: 0, ClassWrite: 1, ClassIrreversible: 2, ClassDelete: 3, ClassMoney: 4}

// Gated reports whether K4 refuses the class until the owner opens it on the
// Card: irreversible, delete and money.
func (c Class) Gated() bool { return classRank[c] >= classRank[ClassIrreversible] }

func worse(a, b Class) Class {
	if classRank[b] > classRank[a] {
		return b
	}
	return a
}

// ClassedTool is what a class is computed from: the tool's name at its vendor
// (planque's name, before KIFF's suffix), its app when the connection names
// one, planque's read_only, and the profile's money mark.
type ClassedTool struct {
	App      string
	Name     string
	ReadOnly bool
	Money    bool
	// Destructive is the vendor's destructiveHint: it only raises.
	Destructive bool
	// Operation is the vendor operation a narrowed tool pins as a fixed
	// argument (stripe_api_write's PostInvoicesInvoiceVoid): it is classed
	// too, and only raises, since the tool's own name says nothing of it.
	Operation string
}

// Classify is the tool's class: the worse of what the rules give and what a
// reviewed table for its app says (classes/<app>.json). Neither can lower the
// other, so a table only ever adds strictness, and a read always needs
// planque's read_only and a read verb.
func Classify(t ClassedTool) Class {
	c := ruleClass(t)
	if tc, ok := tableClass(t.App, t.Name); ok {
		c = worse(c, tc)
	}
	return c
}

// ruleClass is the class from the rules alone: a read needs planque's
// read_only and a read verb; a name decides delete, money and irreversible;
// anything else is a write.
func ruleClass(t ClassedTool) Class {
	if t.Money {
		return ClassMoney
	}
	// A camelCase name (an OpenAPI operation id: searchTickets, postReply) is
	// read as its words joined by underscores, so its verb is a verb.
	name := t.Name
	if !strings.ContainsRune(name, '_') && strings.ToLower(name) != name {
		name = snake(name)
	}
	c := nameClass(t, strings.ToLower(name))
	// A name that starts with its app's name (Composio's STRIPE_VOID_INVOICE)
	// is classed again without it. The whole name can only raise that class;
	// and the name without its app makes a read only as any name does, with
	// planque's read_only too (STRIPE_LIST_INVOICES).
	if i := strings.IndexByte(name, '_'); i > 0 {
		n, rest := strings.ToLower(name), strings.ToLower(name[i+1:])
		if !hasPrefix(n, allVerbs()) && hasPrefix(rest, allVerbs()) {
			// The name without its app decides, and the whole name can
			// only raise it (a money or irreversible word anywhere in it).
			whole := c
			c = nameClass(ClassedTool{ReadOnly: t.ReadOnly, Destructive: t.Destructive}, rest)
			if whole != ClassWrite {
				c = worse(c, whole)
			}
		}
	}
	if t.Operation != "" {
		c = worse(c, nameClass(ClassedTool{}, snake(t.Operation)))
	}
	return c
}

// nameClass is the class one name gives: a read only with planque's
// read_only and a read verb; a delete, money or irreversible name; else a
// write.
func nameClass(t ClassedTool, n string) Class {
	// A read verb on a tool planque calls read-only is a read, whatever
	// else its name says: list_cards lists cards and moves no money.
	// A tool its vendor marks destructive is never a read, whatever its
	// other hint says.
	if t.ReadOnly && !t.Destructive && hasPrefix(n, readVerbs) {
		return ClassRead
	}
	c := ClassWrite
	if hasPrefix(n, irreversibleVerbs) || containsAny(n, irreversibleWords) || (strings.HasPrefix(n, "create_") && strings.Contains(n, "invoice")) {
		// Creating an invoice may issue it (a vendor that finalises unless
		// told draft): irreversible until a signed draft pin says otherwise.
		c = worse(c, ClassIrreversible)
	}
	if hasPrefix(n, deleteVerbs) {
		c = worse(c, ClassDelete)
	} else if t.Destructive {
		// The vendor's destructiveHint: at least irreversible, or delete
		// for a delete (above).
		c = worse(c, ClassIrreversible)
	}
	if containsAny(n, moneyWords) || n == "approve_request" || n == "decline_request" {
		c = worse(c, ClassMoney)
	}
	return c
}

// allVerbs are every verb a name's class is read from.
func allVerbs() []string {
	out := append([]string{}, readVerbs...)
	out = append(out, irreversibleVerbs...)
	return append(out, deleteVerbs...)
}

// snake writes a CamelCase operation id as words joined by underscores:
// PostInvoicesInvoiceVoid is post_invoices_invoice_void.
func snake(op string) string {
	var b strings.Builder
	for i, r := range op {
		if unicode.IsUpper(r) && i > 0 {
			b.WriteByte('_')
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

var (
	readVerbs         = []string{"get_", "list_", "search_", "find_", "read_", "fetch_", "retrieve_", "lookup_", "describe_", "count_"}
	deleteVerbs       = []string{"delete_", "remove_", "destroy_", "purge_", "erase_"}
	irreversibleVerbs = []string{"send_", "finalize_", "finalise_", "issue_", "invite_", "share_", "publish_", "submit_", "approve_", "sign_", "void_", "cancel_", "close_", "mark_", "unmark_"}
	irreversibleWords = []string{"_status", "finaliz", "finalis", "credit_note", "reminder", "membership", "_invite", "_share", "_void", "uncollectible", "_send"}
	moneyWords        = []string{"transfer", "payment", "payout", "refund", "charge", "card", "pay_", "_pay", "withdraw", "deposit", "money_request"}
)

func hasPrefix(s string, ps []string) bool {
	for _, p := range ps {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func containsAny(s string, ws []string) bool {
	for _, w := range ws {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// NarrowOpenings are the one-value openings the tool's app table gives it,
// or none: then a Card opens its class whole.
func (t Tool) NarrowOpenings() []NarrowOpening {
	tt, ok := tableEntry(t.App, t.classedName())
	if !ok {
		return nil
	}
	return tt.narrow
}

// ClassParam is the parameter KIFF is told the call's class in (K4). The
// gateway sets it from the tool, after the agent's arguments, so an agent
// can't name its own (apps/api GatewayClassParam).
const ClassParam = "kiff_class"

// Class is the tool's class (Classify), from its name at the vendor, its
// app, planque's read_only and the profile's money mark.
func (t Tool) Class() Class {
	return Classify(ClassedTool{App: t.App, Name: t.classedName(), ReadOnly: t.ReadOnly, Money: t.Money, Destructive: t.DestructiveHint, Operation: t.pinnedOperation()})
}

// Conditioned says whether its app's reviewed table has a condition that can
// lower this tool's class on a call (create_client_invoice with status:
// draft).
func (t Tool) Conditioned() bool {
	tt, ok := tableEntry(t.App, t.classedName())
	return ok && tt.condition != nil
}

// operationArgs are the fixed arguments a narrowed tool pins its vendor
// operation in.
var operationArgs = []string{"stripe_api_operation_id"}

// pinnedOperation is the vendor operation the tool's fixed arguments pin,
// or "".
func (t Tool) pinnedOperation() string {
	fixed, err := t.fixedArgs()
	if err != nil {
		return ""
	}
	for _, k := range operationArgs {
		if op, ok := fixed[k].(string); ok {
			return op
		}
	}
	return ""
}

// classedName is the tool's name at its vendor: what its class is read from.
func (t Tool) classedName() string {
	switch {
	case t.VendorTool != "":
		return t.VendorTool
	case t.UpstreamTool != "":
		return t.UpstreamTool
	}
	return t.Name
}

// ClassArgsParam carries a call's values for the arguments an opening can
// name (apps/api GatewayClassArgsParam).
const ClassArgsParam = "kiff_class_args"

//go:embed classes/*.json
var classFiles embed.FS

// ClassTable is a reviewer's file of an app's tools and their classes
// (classes/<app>.json): each tool's name at the vendor and its class, and a
// condition under which a call of it is another class (create_client_invoice
// with status draft is a write), plus the categories' openings, whose
// arguments a call's class carries to KIFF.
type ClassTable struct {
	App   string `json:"app"`
	Tools []struct {
		Name      string          `json:"name"`
		Class     Class           `json:"class"`
		Condition *classCondition `json:"condition"`
		Operation *classOperation `json:"operation"`
	} `json:"tools"`
	Categories map[string]struct {
		Opens []struct {
			Name      string          `json:"name"`
			Condition *classCondition `json:"condition"`
		} `json:"opens"`
	} `json:"categories"`
}

type classCondition struct {
	Argument string `json:"argument"`
	Equals   string `json:"equals"`
	Then     Class  `json:"then"`
}

// classOperation is a tool whose call does one of several things, each a
// flag of its own (change_client_invoice_status: finalize, cancel,
// unmark_as_paid). The call's operation, under Name, is the one flag it sets
// true, or OperationMixed for none or more than one. KIFF derives it from
// the flags and never reads it from the call: an agent that sends its own
// "operation" next to cancel: true matches nothing.
type classOperation struct {
	Name  string   `json:"name"`
	OneOf []string `json:"one_of"`
}

// OperationMixed is the operation of a call that sets none of its tool's
// flags, or more than one: no opening matches it.
const OperationMixed = "mixed"

// of is the call's operation, from its flags alone. It fails closed: a
// flag counts as set unless it's absent, null or false, so a flag sent as
// "true", 1 or anything else beside finalize makes the call mixed, never
// finalize.
func (o *classOperation) of(callArgs map[string]any) string {
	set := ""
	for _, flag := range o.OneOf {
		if v, present := callArgs[flag]; present && v != nil && v != false {
			if set != "" {
				return OperationMixed
			}
			set = flag
		}
	}
	if set == "" {
		return OperationMixed
	}
	return set
}

type tableTool struct {
	class     Class
	condition *classCondition
	operation *classOperation
	// args are the arguments an opening or condition names for this tool.
	args []string
	// narrow are the openings the table's categories give this tool for one
	// value (send_invoices: change_client_invoice_status for operation =
	// finalize), the narrowest a Card's draft is given.
	narrow []NarrowOpening
}

// NarrowOpening is an opening for one argument's value, as a reviewed
// table's category names it.
type NarrowOpening struct {
	Argument string `json:"argument"`
	Value    string `json:"value"`
}

var (
	tablesOnce sync.Once
	tables     map[string]map[string]tableTool
	tablesErr  error
)

func loadTables() {
	tables = map[string]map[string]tableTool{}
	entries, err := classFiles.ReadDir("classes")
	if err != nil {
		tablesErr = err
		return
	}
	for _, e := range entries {
		raw, err := classFiles.ReadFile("classes/" + e.Name())
		if err != nil {
			tablesErr = err
			return
		}
		var t ClassTable
		if err := json.Unmarshal(raw, &t); err != nil {
			tablesErr = fmt.Errorf("classes/%s: %w", e.Name(), err)
			return
		}
		app := strings.ToLower(strings.TrimSpace(t.App))
		m := map[string]tableTool{}
		for _, tool := range t.Tools {
			if _, ok := classRank[tool.Class]; !ok {
				tablesErr = fmt.Errorf("classes/%s: %s has class %q", e.Name(), tool.Name, tool.Class)
				return
			}
			tt := tableTool{class: tool.Class, condition: tool.Condition, operation: tool.Operation}
			if o := tool.Operation; o != nil && (o.Name == "" || len(o.OneOf) == 0 || slices.Contains(o.OneOf, o.Name) || slices.Contains(o.OneOf, OperationMixed)) {
				tablesErr = fmt.Errorf("classes/%s: %s has an operation that isn't one", e.Name(), tool.Name)
				return
			}
			if c := tool.Condition; c != nil {
				if _, ok := classRank[c.Then]; !ok || c.Argument == "" {
					tablesErr = fmt.Errorf("classes/%s: %s has a condition that isn't one", e.Name(), tool.Name)
					return
				}
				tt.args = append(tt.args, c.Argument)
			}
			m[tool.Name] = tt
		}
		for _, cat := range t.Categories {
			for _, o := range cat.Opens {
				tt, ok := m[o.Name]
				if !ok || o.Condition == nil || o.Condition.Argument == "" {
					continue
				}
				tt.args = append(tt.args, o.Condition.Argument)
				tt.narrow = append(tt.narrow, NarrowOpening{Argument: o.Condition.Argument, Value: o.Condition.Equals})
				m[o.Name] = tt
			}
		}
		tables[app] = m
	}
}

// ClassTablesErr is why the embedded tables couldn't be read, or nil; a
// process with a bad table refuses to start (cmd/gateway).
func ClassTablesErr() error {
	tablesOnce.Do(loadTables)
	return tablesErr
}

func tableEntry(app, name string) (tableTool, bool) {
	tablesOnce.Do(loadTables)
	if tablesErr != nil || app == "" {
		return tableTool{}, false
	}
	t, ok := tables[strings.ToLower(app)][name]
	return t, ok
}

func tableClass(app, name string) (Class, bool) {
	t, ok := tableEntry(app, name)
	return t.class, ok
}

// CallClass is one call's class: the tool's, or, when its app's table has a
// condition the call's arguments meet (status: draft), the condition's
// class. A condition may lower the tool's class only to what the reviewer
// wrote, and only on the value the call itself sends, which is the value
// signed into its permit. args are the call's values for the arguments the
// table names, which KIFF matches an opening against.
func (t Tool) CallClass(callArgs map[string]any) (Class, map[string]string) {
	c := t.Class()
	tt, ok := tableEntry(t.App, t.classedName())
	if !ok {
		return c, nil
	}
	// The call's own value for each argument the table names, but the
	// operation: that one is derived from its flags, never taken as sent.
	var args map[string]string
	if o := tt.operation; o != nil {
		args = map[string]string{o.Name: o.of(callArgs)}
	}
	for _, a := range tt.args {
		if tt.operation != nil && a == tt.operation.Name {
			continue
		}
		if v, ok := callArgs[a].(string); ok {
			if args == nil {
				args = map[string]string{}
			}
			args[a] = v
		}
	}
	if cond := tt.condition; cond != nil {
		// The reviewer's condition for this exact tool stands over the
		// vendor's destructiveHint as it does over the name. Only the
		// top-level argument is read: a draft create can still
		// carry nested send, finalise or e-invoicing fields. The relay's own
		// draft-only check refuses those, and must stay until KIFF signs the
		// whole vendor body for pinned arguments.
		v, ok := callArgs[cond.Argument].(string)
		if tt.operation != nil && cond.Argument == tt.operation.Name {
			v, ok = args[cond.Argument], true
		}
		// A value the permit signs at that path is the one sent: the relay
		// checks its final body against it. The call's own argument is read
		// only when no pin is signed there.
		if pin, signed := t.signedPin(cond.Argument); signed {
			v, ok = pin.(string)
		}
		if ok && v == cond.Equals {
			c = worse(cond.Then, minimumOf(ClassedTool{ReadOnly: t.ReadOnly, Money: t.Money}))
		}
	}
	return c, args
}

// minimumOf is the floor no condition goes under: money when the profile
// marks money, a read only when planque says read-only, else a write.
func minimumOf(t ClassedTool) Class {
	switch {
	case t.Money:
		return ClassMoney
	case t.ReadOnly:
		return ClassRead
	default:
		return ClassWrite
	}
}
