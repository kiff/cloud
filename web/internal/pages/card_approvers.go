package pages

import (
	"github.com/kiff/cloud/web/internal/apiclient"
	"strconv"
	"strings"
)

type ApproverScope struct {
	Action, Parameter, Unit string
	Scale                   int
	PerAction               int64
}

func (s ApproverScope) Label() string {
	if s.Unit != "" {
		return s.Action + " · " + s.Unit
	}
	return s.Action + " · " + s.Parameter + " (stored units)"
}
func (s ApproverScope) Step() string {
	if s.Scale <= 0 {
		return "1"
	}
	return "0." + strings.Repeat("0", s.Scale-1) + "1"
}
func (s ApproverScope) Suggested() string { return scaledNumber(s.PerAction, s.Scale) }
func approverAmount(g apiclient.ApproverGrant, scopes []ApproverScope) string {
	for _, s := range scopes {
		if strings.EqualFold(s.Action, g.Action) && strings.EqualFold(s.Parameter, g.Parameter) && s.Unit != "" {
			return scaledNumber(g.Max, s.Scale) + " " + s.Unit
		}
	}
	return strconv.FormatInt(g.Max, 10) + " " + g.Parameter + " (stored units)"
}
func approverStatus(g apiclient.ApproverGrant) string {
	switch g.ConnectionStatus {
	case "connected":
		return "Connected"
	case "awaiting_connection":
		return "Awaiting connection"
	case "not_responding":
		return "Not responding"
	case "expired":
		return "Expired"
	case "revoked":
		return "Revoked"
	case "inactive":
		return "Authority inactive"
	}
	return "Status unavailable"
}
func externalAnswerName(a *apiclient.ExternalAnswer) string {
	if a.Name != "" {
		return a.Name
	}
	return a.Principal
}
func externalAnswerReason(a *apiclient.ExternalAnswer) string {
	if a.Reason != "" {
		return a.Reason
	}
	return strings.ReplaceAll(a.ReasonCode, "_", " ")
}
