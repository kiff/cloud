package executors

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/kiff/kiff/pkg/kiff/action"
)

// ExecutorFunc is the function signature the runtime expects for
// every action executor. It mirrors the inline type on
// action.ActionContract.Executor; we declare it here so registry
// callers do not have to repeat the type.
type ExecutorFunc = func(context.Context, action.ActionContext) (action.ActionResult, error)

// Registry maps a string key to an action executor. Tenants
// reference executors from kiff.yaml by these keys; the cloud binary
// controls which keys are registered. Tenants cannot register
// executors at runtime.
//
// Unknown keys must fail the YAML validation path. Callers
// (internal/domain) check membership with Has and only call
// Get after validation.
type Registry struct {
	mu  sync.RWMutex
	fns map[string]ExecutorFunc
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{fns: map[string]ExecutorFunc{}}
}

// Register binds a key to an executor function. Calling Register
// twice for the same key is rejected; the cloud binary should
// register every executor exactly once at startup.
func (r *Registry) Register(key string, fn ExecutorFunc) error {
	if key == "" {
		return fmt.Errorf("registry: key is required")
	}
	if fn == nil {
		return fmt.Errorf("registry: executor for %q is nil", key)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.fns[key]; exists {
		return fmt.Errorf("registry: executor %q already registered", key)
	}
	r.fns[key] = fn
	return nil
}

// MustRegister is the panicking variant. Used at process startup
// where a duplicate key indicates a programming error and is fatal.
func (r *Registry) MustRegister(key string, fn ExecutorFunc) {
	if err := r.Register(key, fn); err != nil {
		panic(err)
	}
}

// Has reports whether key is registered. Used by the YAML validator
// to surface unknown keys with a precise error.
//
// A nil registry has nothing registered. ParseAndBuild documents a nil
// registry as "parse without checking executor keys", and a caller that
// only wants a domain's shape — the mandate validator, for one — passes
// one. Reporting that as "not registered" is what the validator already
// assumed; panicking on it was a crash waiting for the first caller who
// took the documentation at its word.
func (r *Registry) Has(key string) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.fns[key]
	return ok
}

// Get returns the executor for key. Returns nil and false if the key
// is not registered; callers must Has-check first.
//
// Nil-safe for the same reason as Has: a domain parsed without a
// registry has no executor functions, and its actions carry a nil
// Executor. That is correct for a caller reading the shape of a
// contract, and a caller that means to run one has a registry.
func (r *Registry) Get(key string) (ExecutorFunc, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	fn, ok := r.fns[key]
	return fn, ok
}

// Keys returns the registered keys in lexicographic order. Used by
// kiff-tenant for diagnostic listings.
func (r *Registry) Keys() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.fns))
	for k := range r.fns {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// DefaultRegistry returns a Registry populated with the cloud's v0.1
// demo executors. The cloud binary calls this at startup so the
// registry is consistent across process restarts. Adopters who want
// a different mix construct their own registry.
func DefaultRegistry() *Registry {
	r := NewRegistry()
	r.MustRegister("refund.mark_paid", MarkPaid)
	r.MustRegister("refund.issue_refund", IssueRefund)
	r.MustRegister("refund.auto_refund", AutoRefund)
	r.MustRegister("support.escalate", Escalate)
	r.MustRegister("support.close_ticket", CloseTicket)
	r.MustRegister("support.send_outreach", SendOutreach)
	r.MustRegister("support.waive_fee", WaiveFee)

	// ai-cafe-ops (kiff/kiff#3) — cloud-side counterpart of the
	// framework's worked example. The auto-vs-approval routing for
	// inventory orders lives client-side; here we expose both
	// contracts and let the runtime gate by approval requirement.
	r.MustRegister("cafe.start_shift", CafeStartShift)
	r.MustRegister("cafe.auto_order_inventory", CafeAutoOrderInventory)
	r.MustRegister("cafe.order_inventory", CafeOrderInventory)
	r.MustRegister("cafe.request_specialty", CafeRequestSpecialty)
	r.MustRegister("cafe.send_staff_message", CafeSendStaffMessage)
	r.MustRegister("cafe.escalate_supplier", CafeEscalateSupplier)

	// cloud.proxy is the generic executor the guided domain
	// builder emits for tenant-authored YAMLs. Customers
	// run their own side-effect in their app after KIFF returns
	// allowed; cloud.proxy exists so the YAML validator's
	// executor-key check accepts user-built domains.
	r.MustRegister("cloud.proxy", CloudProxy)

	return r
}
