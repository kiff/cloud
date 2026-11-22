package main

import (
	"os"
	"regexp"
	"testing"
)

// The deploy script sets KIFF_NOTIFY_FROM and KIFF_NOTIFY_REPLY_TO in production, so its defaults are the
// ones that send; the server's own are the same, or an email goes out under the old sender whatever the
// code says (kiff/cloud#104: the script still read "KIFF Operations <ops@kiff.dev>").
func TestTheDeployScriptAndTheServerSendAsTheSameSender(t *testing.T) {
	read := func(path string) string {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Skipf("%s is not here: %v", path, err)
		}
		return string(b)
	}
	script, server := read("../../../scripts/deploy.sh"), read("main.go")
	pick := func(src, pattern, what string) string {
		m := regexp.MustCompile(pattern).FindStringSubmatch(src)
		if m == nil {
			t.Fatalf("no %s default found", what)
		}
		return m[1]
	}
	fromScript := pick(script, `KIFF_NOTIFY_FROM="\$\{KIFF_NOTIFY_FROM:-([^}]*)\}"`, "script sender")
	fromServer := pick(server, `from = "([^"]*)"`, "server sender")
	if fromScript != fromServer || fromServer != "KIFF <hello@kiff.dev>" {
		t.Errorf("the sender: script %q, server %q, want %q in both", fromScript, fromServer, "KIFF <hello@kiff.dev>")
	}
	replyScript := pick(script, `KIFF_NOTIFY_REPLY_TO="\$\{KIFF_NOTIFY_REPLY_TO:-([^}]*)\}"`, "script reply-to")
	replyServer := pick(server, `replyTo = "([^"]*)"`, "server reply-to")
	if replyScript != replyServer || replyServer != "support@kiff.dev" {
		t.Errorf("the reply-to: script %q, server %q, want support@kiff.dev in both", replyScript, replyServer)
	}
}
