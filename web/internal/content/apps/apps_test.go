package apps

import "testing"

// Every app listed as working has a way to connect: the site never lists an
// app we can't connect.
func TestEveryWorkingAppHasAConnectMethod(t *testing.T) {
	if len(Working()) == 0 {
		t.Fatal("no app works: the list is empty or mis-read")
	}
	for _, a := range Working() {
		if a.Connects != ConnectsKey && a.Connects != ConnectsSignIn {
			t.Errorf("%s works but has no connect method (%q)", a.ID, a.Connects)
		}
	}
}

func TestValidateRefusesBadEntries(t *testing.T) {
	for name, as := range map[string][]App{
		"no connect method": {{ID: "x", Name: "X", Status: StatusWorks}},
		"unknown status":    {{ID: "x", Name: "X", Connects: ConnectsKey, Status: "ready"}},
		"coming, no method": {{ID: "x", Name: "X", Status: StatusComing}},
		"not yet, a method": {{ID: "x", Name: "X", Connects: ConnectsSignIn, Status: StatusNotYet}},
		"duplicate":         {{ID: "x", Name: "X", Connects: ConnectsKey, Status: StatusWorks}, {ID: "x", Name: "X", Connects: ConnectsKey, Status: StatusComing}},
		"no name":           {{ID: "x", Connects: ConnectsKey, Status: StatusWorks}},
	} {
		if validate(as) == nil {
			t.Errorf("%s: validate accepted %+v", name, as)
		}
	}
}

// The seven apps that connect by key in the live matrix.
func TestTheLiveMatrixWorks(t *testing.T) {
	for _, id := range []string{"stripe", "qonto", "shippo", "intercom", "zendesk", "pipedrive", "github"} {
		a, ok := ByID(id)
		if !ok || !a.Works() || a.Connects != ConnectsKey {
			t.Errorf("%s: want a working key app, got %+v (found %v)", id, a, ok)
		}
	}
	if n := len(Working()); n != 11 {
		t.Errorf("%d apps work, want the matrix's 7, Gmail, Typeform and Google's Calendar and Drive", n)
	}
}

// The list names the connect matrix's 20 apps plus LinkedIn, and the five with no
// readable description and no sign-in proved, and Harvest, are not yet, with no method.
func TestTheListMatchesTheMatrix(t *testing.T) {
	if n := len(All()); n != 21 {
		t.Errorf("%d apps, want the matrix's 20 and LinkedIn", n)
	}
	for _, id := range []string{"gorgias", "freshdesk", "quickbooks", "zoho-books", "moneybird", "harvest"} {
		a, ok := ByID(id)
		if !ok || a.Status != StatusNotYet || a.Connects != "" || a.Label() != "Not yet" {
			t.Errorf("%s: want not yet with no method, got %+v (found %v)", id, a, ok)
		}
	}
}

// Gmail's managed sign-in was completed on the owner's phone (13 Nov): it
// works, by a sign-in, and says so. LinkedIn, for go-to-market job 3, is
// listed as coming until a sign-in proves it.
func TestGmailWorksBySignInAndLinkedInIsComing(t *testing.T) {
	g, ok := ByID("gmail")
	if !ok || !g.Works() || g.Connects != ConnectsSignIn || g.Label() != "Connects with a sign-in" {
		t.Errorf("gmail: %+v %q", g, g.Label())
	}
	l, ok := ByID("linkedin")
	if !ok || l.Works() || l.Status != StatusComing || l.Connects != ConnectsSignIn {
		t.Errorf("linkedin: %+v", l)
	}
}

// Typeform's managed sign-in was completed on a phone (16 Nov): connected, and
// one read-only call listed its forms. The account was in Typeform's US region;
// an account whose data is kept in its EU data centre uses another API address
// and isn't proved yet.
func TestTypeformWorksBySignIn(t *testing.T) {
	a, ok := ByID("typeform")
	if !ok || !a.Works() || a.Connects != ConnectsSignIn || a.Label() != "Connects with a sign-in" {
		t.Errorf("typeform: %+v %q", a, a.Label())
	}
}

// Google Calendar and Drive: each managed sign-in was completed on a phone on
// a test account (16 Nov), connected, and one read-only call answered. Their
// sign-ins ask Google for more than a read-only job uses, and the connection
// card says so; they still connect. Google Sheets connected too, but is coming
// until Coach stops calling its sign-in read-only (that fix is not live yet).
func TestGoogleCalendarAndDriveWorkBySignIn(t *testing.T) {
	for _, id := range []string{"google-calendar", "google-drive"} {
		a, ok := ByID(id)
		if !ok || !a.Works() || a.Connects != ConnectsSignIn || a.Label() != "Connects with a sign-in" {
			t.Errorf("%s: %+v %q", id, a, a.Label())
		}
	}
	if s, ok := ByID("google-sheets"); !ok || s.Works() || s.Status != StatusComing {
		t.Errorf("google-sheets: %+v", s)
	}
}
