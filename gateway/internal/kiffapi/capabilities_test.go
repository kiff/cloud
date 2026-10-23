package kiffapi

import "testing"

// A person connects tools when their role holds tools.connect
// (an admin or editor), whatever their role names; a key from an API
// that sends no capabilities keeps the role rule.
func TestCanManageReadsCapabilities(t *testing.T) {
	for name, c := range map[string]struct {
		me   Me
		want bool
	}{
		"editor":                 {Me{Roles: []string{"editor", "operator"}, Capabilities: []string{"read", "tools.connect"}}, true},
		"approver":               {Me{Roles: []string{"approver"}, Capabilities: []string{"read", "holds.answer"}}, false},
		"admin key, older API":   {Me{Roles: []string{"admin"}}, true},
		"runtime key, older API": {Me{Roles: []string{"ops_agent"}}, false},
		"agent key with the cap": {Me{AgentID: "a1", Capabilities: []string{"tools.connect"}}, false},
		"phone of an admin":      {Me{Roles: []string{"admin"}, Capabilities: []string{"read", "tools.connect"}, DeviceSession: true}, false},
		// The key planque keeps connects tools.
		"connector key": {Me{APIKey: true, Roles: []string{"connector"}, Capabilities: []string{"read", "personal", "tools.connect"}}, true},
	} {
		if got := c.me.CanManage(); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}

// A phone's session reads the tools its person could
// manage, and manages none; nobody else reads without managing.
func TestCanReadLetsAPhoneList(t *testing.T) {
	for name, c := range map[string]struct {
		me   Me
		want bool
	}{
		"editor":            {Me{Capabilities: []string{"tools.connect"}}, true},
		"phone of an admin": {Me{Capabilities: []string{"read", "tools.connect"}, DeviceSession: true}, true},
		"phone of approver": {Me{Capabilities: []string{"read", "holds.answer"}, DeviceSession: true}, false},
		"approver":          {Me{Capabilities: []string{"read", "holds.answer"}}, false},
		"agent key":         {Me{AgentID: "a1", Capabilities: []string{"tools.connect"}, DeviceSession: true}, false},
	} {
		if got := c.me.CanRead(); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}
