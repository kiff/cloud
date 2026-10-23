package gateway

import (
	"fmt"
	"regexp"
	"strings"
)

// Role is the role the contract grants every tool's permission to. A
// gateway key is a standard API key bound to one agent and holding only
// this role.
const Role = "gateway_agent"

// SetupReaderRole is the role the contract grants the read-only tools'
// permissions to, and nothing else: the key of an agent that holds no tools
// (Setup, kiff/agents#91) carries it, so it can read a tool the owner
// approved for it to read (kiff/tafs#138, gate A). Each read is still
// decided on a Card naming that agent, as any call is.
const SetupReaderRole = "setup_reader"

var toolName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

var amountUnit = regexp.MustCompile(`^[A-Za-z]{1,16}$`)

// ValidAmountUnit reports whether unit and scale can describe an amount
// argument: letters only (a currency code such as EUR, or a word), and
// 0 to 6 decimal places. The same rule apps/api applies to the domain.
func ValidAmountUnit(unit string, scale int) bool {
	return amountUnit.MatchString(unit) && scale >= 0 && scale <= 6
}

// ValidToolName reports whether name can be a tool or argument name in
// the generated contract.
func ValidToolName(name string) bool { return toolName.MatchString(name) }

// Contract is the account's gateway domain: one ToolCall
// entity with one state, and one action per connected tool. Each action:
//   - names cloud.proxy, the registered no-op executor, because the
//     gateway, not apps/api, does the forwarding;
//   - declares the tool's amount argument as an int, so a Card can sum it,
//     and its resource argument as a string, so a Card can name the
//     resources it applies to;
//   - requires issued authority, so a tool no Card covers is refused;
//   - grants every tool to Role, and the read-only ones (risk: low) to
//     SetupReaderRole too.
//
// It is applied through apps/api's domain update, never written to
// Postgres directly, so the running API drops its cached copy.
func Contract(tools []Tool) (string, error) {
	var b strings.Builder
	b.WriteString(`domain: ` + Domain + `
entity: ToolCall
events: [TOOL_CALL_REQUESTED]
states: [requested]
transitions:
  - on: TOOL_CALL_REQUESTED
    from: ""
    to: requested
actions:
`)
	perms := make([]string, 0, len(tools))
	var reads []string
	for _, t := range tools {
		if !toolName.MatchString(t.Name) {
			return "", fmt.Errorf("gateway: tool name %q must be a letter followed by letters, digits or underscores", t.Name)
		}
		if t.AmountArg != "" && !toolName.MatchString(t.AmountArg) {
			return "", fmt.Errorf("gateway: amount argument %q of %s is not a valid parameter name", t.AmountArg, t.Name)
		}
		if t.ResourceArg != "" && (!toolName.MatchString(t.ResourceArg) || t.ResourceArg == t.AmountArg || t.ResourceArg == OperationArg) {
			return "", fmt.Errorf("gateway: resource argument %q of %s is not a valid parameter name", t.ResourceArg, t.Name)
		}
		perm := "tools." + t.Name
		perms = append(perms, perm)
		fmt.Fprintf(&b, "  - name: %s\n    allowed_states: [requested]\n", t.Name)
		var required []string
		for _, a := range []string{t.AmountArg, t.ResourceArg} {
			if a != "" {
				required = append(required, a)
			}
		}
		if len(required) > 0 {
			fmt.Fprintf(&b, "    required_parameters: [%s]\n    parameters:\n", strings.Join(required, ", "))
		}
		if t.AmountArg != "" {
			fmt.Fprintf(&b, "      - name: %s\n        type: int\n        min: 0\n", t.AmountArg)
			if t.AmountUnit != "" {
				if !ValidAmountUnit(t.AmountUnit, t.AmountScale) {
					return "", fmt.Errorf("gateway: amount unit %q (scale %d) of %s is not valid", t.AmountUnit, t.AmountScale, t.Name)
				}
				fmt.Fprintf(&b, "        unit: %s\n        scale: %d\n", t.AmountUnit, t.AmountScale)
			}
		}
		if t.ResourceArg != "" {
			fmt.Fprintf(&b, "      - name: %s\n        type: string\n        min_length: 1\n        max_length: %d\n", t.ResourceArg, MaxResourceLength)
		}
		risk := "medium"
		if t.ReadOnly {
			risk = "low"
			reads = append(reads, perm)
		}
		fmt.Fprintf(&b, "    required_permissions: [%s]\n    risk: %s\n    approval: never\n    executor: cloud.proxy\n    requires_authority: true\n", perm, risk)
	}
	fmt.Fprintf(&b, "permissions:\n  roles:\n    %s: [%s]\n", Role, strings.Join(perms, ", "))
	if len(reads) > 0 {
		fmt.Fprintf(&b, "    %s: [%s]\n", SetupReaderRole, strings.Join(reads, ", "))
	}
	return b.String(), nil
}
