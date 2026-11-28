package handlers

import (
	"context"

	"github.com/kiff/cloud/api/internal/domain"
	"github.com/kiff/cloud/api/internal/mandate"
	"github.com/kiff/cloud/api/internal/words"
	kiffproposal "github.com/kiff/kiff/pkg/kiff/proposal"
)

// GatewayClassParam is the parameter the gateway tells KIFF a call's class in
// (K4, gateway.ClassParam): read, write, irreversible, delete or money. The
// gateway sets it from the tool, over anything the agent sent, and only the
// gateway's domain reads it; it is taken out before the call is recorded,
// as the owner ask is. GatewayClassArgsParam carries the call's values for
// the arguments an opening can name.
const (
	GatewayClassParam     = "kiff_class"
	GatewayClassArgsParam = "kiff_class_args"
)

// callClass is a call's class as the gateway sent it.
type callClass struct {
	class string
	args  map[string]string
}

// takeClass takes the class parameters out of a gateway call's parameters,
// leaving the caller's map as it was, and says the call's class.
func takeClass(spec domain.Spec, prop kiffproposal.ActionProposal) (callClass, kiffproposal.ActionProposal) {
	c, okClass := prop.Parameters[GatewayClassParam]
	a, okArgs := prop.Parameters[GatewayClassArgsParam]
	if !okClass && !okArgs {
		return callClass{}, prop
	}
	params := make(map[string]any, len(prop.Parameters))
	for k, p := range prop.Parameters {
		if k != GatewayClassParam && k != GatewayClassArgsParam {
			params[k] = p
		}
	}
	prop.Parameters = params
	if spec.Domain != gatewayDomain {
		return callClass{}, prop
	}
	out := callClass{}
	out.class, _ = c.(string)
	if m, ok := a.(map[string]any); ok {
		out.args = map[string]string{}
		for k, v := range m {
			if s, ok := v.(string); ok {
				out.args[k] = s
			}
		}
	}
	return out, prop
}

type classKey struct{}

func withClass(ctx context.Context, c callClass) context.Context {
	return context.WithValue(ctx, classKey{}, c)
}

func classFrom(ctx context.Context) callClass {
	c, _ := ctx.Value(classKey{}).(callClass)
	return c
}

// classInto sets K4 on req for an account K4 is on for: the call's class and
// its argument values. A money or irreversible call there always asks its
// owner, whatever the Card opens: a payment, a finalised document or a send
// is approved on the phone, each time. An account off the list is decided
// as before. A call the gateway sent no class for, on a K4 account, is a
// write: never a read by default.
func (h *ProposalsHandler) classInto(ctx context.Context, tenant string, req *mandate.Request) {
	if !h.k4[tenant] {
		return
	}
	c := classFrom(ctx)
	req.K4, req.Class, req.ClassArgs = true, c.class, c.args
	if req.Class == "" {
		req.Class = "write"
	}
	if (req.Class == mandate.ClassMoney || req.Class == mandate.ClassIrreversible) && req.OwnerAsk == "" {
		req.OwnerAsk = words.OwnerAskEachCall
		req.OwnerAskWhy = ownerAskLine(words.OwnerAskEachCall)
	}
}
