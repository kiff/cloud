package gateway

import (
	"regexp"

	"github.com/kiff/cloud/gateway/internal/invocation"
)

// Classes of how a call ended, for the outcome line: the journal outlives
// the call's record and erasure never reaches it, so the line carries a
// class and codes, never the tool's or KIFF's words.
const (
	ClassDone              = "done"               // delivered, the tool answered without an error
	ClassVendorError       = "vendor_error"       // delivered, the tool (the vendor, or the relay for it) answered an error
	ClassConnectionRefusal = "connection_refusal" // a relay connection refused it before sending anything
	ClassRefused           = "kiff_refused"       // KIFF refused it: not sent
	ClassFailed            = "kiff_failed"        // KIFF could not send it: not sent
	ClassUnknown           = "unknown"            // sent, the outcome lost
)

// outcomeClass is the class of an ended call.
func outcomeClass(in invocation.Invocation) string {
	switch in.State {
	case invocation.StateForwarded:
		switch {
		case in.ConnectionRefusal != "":
			return ClassConnectionRefusal
		case in.ToolError != "":
			return ClassVendorError
		}
		return ClassDone
	case invocation.StateRefused:
		return ClassRefused
	case invocation.StateFailed:
		return ClassFailed
	}
	return ClassUnknown
}

// vendorStatus is the HTTP status a tool's error text names, as planque
// and the connectors write it ("Stripe answered 404: …", "planque
// answered 403: …"), or "" when it names none. Three digits, nothing
// else of the text.
var vendorStatus = regexp.MustCompile(`(?i)\banswered ([1-5]\d\d)\b`)

func statusOf(toolError string) string {
	m := vendorStatus.FindStringSubmatch(toolError)
	if m == nil {
		return ""
	}
	return m[1]
}
