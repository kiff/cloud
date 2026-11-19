package handlers

import (
	"context"
	"log/slog"
	"testing"

	"github.com/kiff/cloud/api/internal/personlang"
	"github.com/kiff/cloud/api/internal/tenant"
)

// A re-link is approved in a browser, which sends no app language: its
// email comes in the language the person's app last showed them, and the
// phone with no name is "your phone" in it.
func TestRelinkNoticeIsInThePersonsLanguage(t *testing.T) {
	n := &memberNotifier{}
	h := &DeviceAuthHandler{emails: emailsBySubject{}, notices: n, logger: slog.Default(),
		tenants: oneTenant{t: tenant.Tenant{ID: "t1", Metadata: map[string]any{
			personlang.MetadataKey: map[string]any{"people": map[string]any{"ana": "es"}}}}}}
	h.relinkNotice(context.Background(), "t1", "ana", "")
	if len(n.notices) != 1 || n.notices[0] != "ana@x.io|Tu teléfono puede volver a aprobar, con una clave nueva" {
		t.Fatalf("notices = %v", n.notices)
	}
	// Without the account, English, as before.
	n2 := &memberNotifier{}
	(&DeviceAuthHandler{emails: emailsBySubject{}, notices: n2, logger: slog.Default()}).relinkNotice(context.Background(), "t1", "ana", "Pixel")
	if len(n2.notices) != 1 || n2.notices[0] != "ana@x.io|Pixel can approve again, with a new key" {
		t.Fatalf("english = %v", n2.notices)
	}
}
