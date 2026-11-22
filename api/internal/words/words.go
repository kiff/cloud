// Package words is KIFF's lines for people in the eight languages tafs
// speaks (kiff/tafs/docs/languages.md, the one glossary): English, German,
// French, Spanish, Italian, Portuguese, Dutch and Polish. English is the
// source; every other language has every line, and a test fails on one
// that's missing or empty.
//
// The language is the app's own, sent as Accept-Language: an app that
// ships only English sends "en" whatever the phone says, so KIFF's words
// match the screen's. A language changes words only: never what a Card
// allows, what counts against a limit, or how an amount is checked.
// Receipts are never translated: their words are signed. Developers'
// words (logs, an API's "error") stay in English.
package words

import (
	"strconv"
	"strings"
	"time"
)

// Lang is one of the eight languages.
type Lang string

const (
	EN Lang = "en"
	DE Lang = "de"
	FR Lang = "fr"
	ES Lang = "es"
	IT Lang = "it"
	PT Lang = "pt"
	NL Lang = "nl"
	PL Lang = "pl"
)

// The reason codes a gateway call asks for approval with (Words.OwnerAsks).
const (
	// OwnerAskSealedMoney: a tool that moves money on a connection where
	// KIFF holds the key, so no permit binds the call at the tool.
	OwnerAskSealedMoney = "sealed_money"
	// OwnerAskSecondRefund: a refund on a charge that already has one,
	// from anyone (thune step one).
	OwnerAskSecondRefund = "second_refund"
	// OwnerAskNoRequestID: a refund whose agent named no request from a
	// connected app, so KIFF can't hand it to one taf (thune step one).
	OwnerAskNoRequestID = "no_request_id"
	// OwnerAskEachCall: an operation whose reviewed profile holds every
	// call for its owner (ask_each_call), such as a website's checkout.
	OwnerAskEachCall = "each_call"
	// OwnerAskOther: any code KIFF doesn't know, or a value that isn't one.
	OwnerAskOther = "other"
)

// OwnerAskCode is code if KIFF has words for it, else OwnerAskOther.
func OwnerAskCode(code string) string {
	if _, ok := Of(EN).OwnerAsks[code]; ok && code != "" {
		return code
	}
	return OwnerAskOther
}

// All are the eight languages, English first.
var All = []Lang{EN, DE, FR, ES, IT, PT, NL, PL}

// FromHeader is the language KIFF writes a person's lines in, from their
// Accept-Language: the tag with the highest q whose primary language is
// one of the eight ("de-AT" reads German), the earlier one on a tie.
// English when none is, or there's no header.
func FromHeader(header string) Lang {
	best, bestQ := EN, 0.0
	for i, part := range strings.Split(header, ",") {
		if i >= 20 {
			break
		}
		tag, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		q := 1.0
		if v, ok := strings.CutPrefix(strings.TrimSpace(params), "q="); ok {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil || f > 1 {
				continue
			}
			q = f
		}
		primary, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(tag)), "-")
		l, ok := known(primary)
		if !ok || q <= bestQ {
			continue
		}
		best, bestQ = l, q
	}
	return best
}

// ReceiptStepsFor is a receipt's step words for a reader's Accept-Language:
// the tag with the highest q whose primary language KIFF has step words in,
// the eight or ReceiptStepsMore's ("ca-ES" reads Catalan, "zh-Hant" the
// Traditional Chinese), the earlier one on a tie. English when none is. A
// reader in one of the more languages gets their receipts' steps in it,
// and KIFF's other lines in English, as FromHeader says.
func ReceiptStepsFor(header string) ReceiptSteps {
	best, bestQ := Of(EN).ReceiptSteps, 0.0
	for i, part := range strings.Split(header, ",") {
		if i >= 20 {
			break
		}
		tag, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		q := 1.0
		if v, ok := strings.CutPrefix(strings.TrimSpace(params), "q="); ok {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil || f > 1 {
				continue
			}
			q = f
		}
		if q <= bestQ {
			continue
		}
		primary, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(tag)), "-")
		if l, ok := known(primary); ok {
			best, bestQ = Of(l).ReceiptSteps, q
		} else if more, ok := ReceiptStepsMore[primary]; ok {
			best, bestQ = more, q
		}
	}
	return best
}

// Parse is a stored language tag as a Lang; English for anything else.
func Parse(tag string) Lang {
	primary, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(tag)), "-")
	if l, ok := known(primary); ok {
		return l
	}
	return EN
}

func known(primary string) (Lang, bool) {
	for _, l := range All {
		if string(l) == primary {
			return l, true
		}
	}
	return "", false
}

// Of is a language's words; English for one KIFF doesn't write.
func Of(l Lang) *Words {
	if w, ok := catalogs[l]; ok {
		return w
	}
	return catalogs[EN]
}

var catalogs = map[Lang]*Words{EN: &en, DE: &de, FR: &fr, ES: &es, IT: &it, PT: &pt, NL: &nl, PL: &pl}

// Words are KIFF's lines for people in one language. Every field is set
// in every language (TestEveryLanguageHasEveryLine).
type Words struct {
	// DecimalComma: amounts are written 150,00 rather than 150.00.
	DecimalComma bool

	// The approval window, as the API answers it: what the next tap
	// asks for.
	WindowNextApproval     string
	WindowAgainUnderMinute string
	WindowNoneNextMinute   string
	WindowNoneMinutes      func(n int) string

	// When a call happened, in the account's zone.
	Today, Yesterday string
	// Months are the month abbreviations, DayDot a dot after the day
	// ("3. Nov."): DayMonth and DateTime write dates with them.
	Months [12]string
	DayDot bool

	// "Did this happen?": the three answers and what each means, and what
	// the owner reads after a No.
	HappenedYes, HappenedYesDetail         string
	HappenedNo, HappenedNoDetail           string
	HappenedNotSure, HappenedNotSureDetail string
	HappenedNoAfter                        string
	// Why KIFF can't tell what became of a call: it was sent and its
	// answer lost, or the vendor refused it without saying; and whether
	// KIFF's reads of the vendor's records haven't settled it (read) or
	// its records can't be searched for it. vendor is the vendor's name,
	// or "" when the profile names none.
	WhyLost      func(vendor string, read bool) string
	WhyRefused   func(vendor string, read bool) string
	WhyProbation string
	// StatusRefused, StatusLost and StatusProbation are a "Did this happen?"
	// call's status line: what is known, and what to check, never a double
	// negative.
	StatusRefused   func(vendor string) string
	StatusLost      func(vendor string) string
	StatusProbation string
	CallAtVendor    func(action, vendor string) string
	CallByTaf       func(taf string) string

	// Why a call wasn't sent, by KIFF's reason code (handlers' callReasons
	// keys): the same codes in every language.
	Reasons map[string]string
	// Why every call of a tool waits for approval, by the reason code the
	// gateway sends (kiff_owner_ask): the same codes in every language. A
	// code KIFF doesn't know reads as OwnerAskOther's line, never as what
	// the caller sent.
	OwnerAsks map[string]string
	// A held call's reason, composed from the breach's own fields.
	ResourceMissing   func(param string) string
	ResourceNotOnCard func(param, value string) string
	OverPerCall       func(draw, limit string) string
	OverTotal         func(draw, limit, used string) string

	// The held-call email (notify/held.go). HeldIntro is its first
	// sentence; agent and action come already escaped for HTML when the
	// email is HTML, and amount is "" or " 4700 amount_eur".
	HeldSubject, HeldNothingDone, HeldOpen, HeldOptions string
	HeldIntro                                           func(agent, action, amount string) string
	HeldSteps                                           func(steps string) string
	HeldExpires                                         func(when string) string
	HeldNoApprove, HeldButtonsNote                      string
	HeldApproveOnce, HeldChangeCard, HeldReject         string

	// A Card warning's reason (mandate.CardWarning), composed in the
	// reader's language from its kind, its share used and its window, and
	// the email it goes in.
	WarnSpent, WarnBurst, WarnRunway string
	WarnUnusual                      func(ratio string) string
	WarnUsed                         func(pct, period string) string
	// Periods are each window's words after WarnUsed's share, by the
	// window's name (mandate.Window*).
	Periods                           map[string]string
	WarnNeverResets, WarnReviewWindow string
	WarnSubject, WarnReview           string

	// The membership emails: the invitation (notify/members.go), and the
	// notices about who can approve, pack and limit requests, and a
	// re-linked phone (handlers' members.go, pack_requests.go and
	// device_relink.go). Each language sets them in members_<lang>.go.
	// The roles (admin, editor, approver, viewer) stay as KIFF names them,
	// as Card does.
	Membership

	// A Card's lines, its amounts, a held call's reason and a limit's
	// pending use (internal/cardwords and the handlers that use it). Each
	// language sets them in cards_<lang>.go.
	Cards

	// An Activity call's lines: what came of
	// it, and who answered it. Each language sets them in
	// activity_<lang>.go.
	Activity

	// The email KIFF sends when a person's account signs in on a device it
	// hasn't seen (notify/new_device.go). Each language sets it in
	// newdevice_<lang>.go.
	NewDeviceEmail

	// A device's name in a person's list of devices (GET /v1/me/devices),
	// by its kind. Each language sets them in newdevice_<lang>.go.
	DeviceNames

	// The emails about signing in with a code sent by email
	// (notify/code_sign_in.go). Each language sets them in
	// codesignin_<lang>.go.
	CodeSignInEmail

	// A receipt's steps, by their code (receipt.StepWords), beside the
	// signed English. Each language sets them in receiptsteps_<lang>.go.
	ReceiptSteps
}

// CodeSignInEmail is the lines of the three code sign-in emails: the code;
// "signing in with a code isn't on" for a person who has it off; and the
// notice that the setting changed. The asked-for details (when, browser,
// place) are the request's, the place the network's guess.
type CodeSignInEmail struct {
	// The code. CodeIntro sits above the code, CodeValid under it.
	CodeSubject, CodeIntro, CodeValid, CodeNeverShare, CodeNotYou string
	// The request's details' labels.
	CodeAskedWhen, CodeAskedBrowser, CodeAskedPlace string
	// Signing in with a code isn't on: how to turn it on.
	CodeOffSubject, CodeOffIntro, CodeOffHow, CodeOffNotYou string
	// The setting was turned on or off.
	CodeTurnedOnSubject, CodeTurnedOnIntro, CodeTurnedOffSubject, CodeTurnedOffIntro, CodeChangedNotYou string
	// CodeWhy is the footer of the two emails a request sends.
	CodeWhy string
}

// ReceiptSteps are the words for a receipt's steps, by their stable code
// (receipt.Codes): what the apps show, with the signed message under it.
// "%s" is the step's free part (a reason, a next step, a tool's status), as
// it was signed.
type ReceiptSteps struct {
	ReceiptStepLines map[string]string
	// ReceiptAskedAgain is a decision the call was asked again for, and
	// got the same.
	ReceiptAskedAgain func(line string) string
}

// ReceiptStep is code's words with its free part, or "" for a code with
// none.
func (r ReceiptSteps) ReceiptStep(code, detail string) string {
	line := r.ReceiptStepLines[code]
	if line == "" {
		return ""
	}
	if strings.Contains(line, "%s") {
		return strings.Replace(line, "%s", detail, 1)
	}
	return line
}

// DeviceNames are a device's names in a person's list, by its kind
// (auth.DeviceKindOf): drawn in the reader's language when the list is
// read, never the language the app had when the device signed in. A device
// of another kind shows by the name it gave.
type DeviceNames struct {
	TafsOnIPhone, TafsOnIPad, TafsOnAndroidPhone, TafsOnAndroidTablet string
}

// DeviceName is a device's name in the list, by kind; given, as the device
// gave it, for a kind with no name of its own.
func (w *Words) DeviceName(kind, given string) string {
	switch kind {
	case "iphone":
		return w.TafsOnIPhone
	case "ipad":
		return w.TafsOnIPad
	case "android_phone":
		return w.TafsOnAndroidPhone
	case "android_tablet":
		return w.TafsOnAndroidTablet
	}
	return given
}

// NewDeviceEmail is the new-device email's lines (see Words): one line, then
// the sign-in's details under their labels. The place is the network's
// guess, so it reads as approximate ("near Paris"), never as fact. Through
// the tafs app, it says "tafs by KIFF", a name the same in every language.
type NewDeviceEmail struct {
	NewDeviceSubject, NewDeviceSubjectTafs string
	NewDeviceIntro, NewDeviceIntroTafs     string
	// The details' labels.
	NewDeviceWhen, NewDeviceDevice, NewDeviceBrowser, NewDeviceMethod, NewDevicePlace string
	// NewDeviceOn is a browser on a device ("Chrome on Android").
	NewDeviceOn func(browser, device string) string
	// NewDeviceZone names a time zone by its city ("Paris time").
	NewDeviceZone func(city string) string
	// NewDeviceNear is the network's guess of the place, with its city
	// ("near Paris, France").
	NewDeviceNear func(place string) string
	// NewDeviceRow is a detail in the text part ("When: …"; French puts a
	// space before the colon).
	NewDeviceRow func(label, value string) string
	// NewDevicePassword is the password sign-in method.
	NewDevicePassword  string
	NewDevicePlaceNote string
	NewDeviceWasYou    string
	// NewDeviceNotYou also asks for a new password, for a password sign-in;
	// NewDeviceNotYouSignOut only asks to sign the device out.
	NewDeviceNotYou, NewDeviceNotYouSignOut string
	NewDeviceSessions                       string
	NewDeviceWhy                            string
}

// Membership are the membership emails' lines (see Words).
type Membership struct {
	// An unnamed account, and a named one quoted as its admins' choice,
	// so a name like "Apple Support" never reads as the sender.
	AnAccount      string
	AccountNamed   func(name string) string
	UnnamedAccount string
	// The invitation: InviteSubject with the account's name ("" for
	// none), InviteIntro with the inviter, the account (AnAccount or
	// AccountNamed), the role and what the role lets them do.
	InviteSubject   func(name string) string
	InviteIntro     func(by, account, role, what string) string
	InviteRoleWhat  map[string]string
	InviteRoleOther string
	// RoleName is each role's word (the glossary's Terms table).
	RoleName      map[string]string
	InviteAccept  func(url string) string
	InviteButton  string
	InviteExpires func(when string) string
	InviteIgnore  string
	// Who did it, when KIFF can't name them by email.
	AnAdmin, Someone, AMember string
	// The notices to an account's admins and approvers.
	PeopleLink       string
	NoticeWhy        string
	AuthoritySubject func(role, account string) string
	AuthorityInvited func(who, role, account, by string) string
	AuthorityChanged func(who, from, role, account, by string) string
	JoinedSubject    func(account string) string
	// Who joined: the address the invitation went to (KIFF has no email
	// for who accepted), a different address that accepted it, or the
	// invited person themselves.
	JoinedUnknown     func(invited, role, account string) string
	JoinedOther       func(invited, who, role, account string) string
	JoinedSelf        func(who, role, account string) string
	CardChangeSubject string
	// agent "" for a Card no agent is named on, entity "" for none.
	CardChangeBody  func(who, agent, action, entity string) string
	CardChangeCards func(cards string) string
	SoDOffSubject   string
	SoDOffBody      func(who, account string) string
	// A phone that approves again with a new key.
	YourPhone     string
	RelinkSubject func(phone string) string
	RelinkBody    func(phone string) string
	// Another phone that approves, enrolled from the browser while the
	// person had an approval device.
	NewApproverSubject func(phone string) string
	NewApproverBody    func(phone string) string
	// A member's request for a pack or a higher spend limit, to the
	// account's billing admins.
	PackAskSubject  string
	PackAskBody     func(who, pack string) string
	RaiseAskSubject string
	RaiseAskBody    func(who string) string
	AskReason       func(reason string) string
	PackAskHow      func(url string) string
	RaiseAskHow     string
}

// DayMonth is a day of another date with its time: "3 Nov 09:14".
func (w *Words) DayMonth(day int, month time.Month, clock string) string {
	d := strconv.Itoa(day)
	if w.DayDot {
		d += "."
	}
	return d + " " + w.Months[month-1] + " " + clock
}

// DateTime is t in UTC with its year: "2 Jan 2006 15:04 UTC".
func (w *Words) DateTime(t time.Time) string {
	t = t.UTC()
	d := strconv.Itoa(t.Day())
	if w.DayDot {
		d += "."
	}
	return d + " " + w.Months[t.Month()-1] + " " + strconv.Itoa(t.Year()) + " " + t.Format("15:04") + " UTC"
}

// DateTimeIn is t in loc with its year: "2 Jan 2006, 15:04". The caller
// names the zone.
func (w *Words) DateTimeIn(t time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	t = t.In(loc)
	d := strconv.Itoa(t.Day())
	if w.DayDot {
		d += "."
	}
	return d + " " + w.Months[t.Month()-1] + " " + strconv.Itoa(t.Year()) + ", " + t.Format("15:04")
}

// WarningReason is a Card warning's reason: its kind ("spent",
// "unusual", "burst", "runway"), the ratio for an unusual one, the share
// of the total used (pct < 0: no total), and its window.
func (w *Words) WarningReason(kind string, ratio float64, pct float64, window string) string {
	var out string
	switch kind {
	case "spent":
		out = w.WarnSpent
	case "unusual":
		out = w.WarnUnusual(w.Number(strconv.FormatFloat(ratio, 'f', 1, 64)))
	case "burst":
		out = w.WarnBurst
	case "runway":
		out = w.WarnRunway
	}
	if pct >= 0 {
		period := w.Periods[window]
		if period == "" {
			period = w.Periods["calendar_day"]
		}
		out += " " + w.WarnUsed(strconv.FormatFloat(pct, 'f', 0, 64), period)
	}
	if window == "lifetime" {
		return out + " " + w.WarnNeverResets
	}
	return out + " " + w.WarnReviewWindow
}

// Number writes a decimal amount ("150.00") the language's way.
func (w *Words) Number(decimal string) string {
	if w.DecimalComma {
		return strings.Replace(decimal, ".", ",", 1)
	}
	return decimal
}

// Capitalize upper-cases a line's first letter.
func Capitalize(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

// plFew is Polish's "few" form for n: 2–4, 22–24, … but not 12–14.
func plFew(n int) bool {
	return n%10 >= 2 && n%10 <= 4 && (n%100 < 12 || n%100 > 14)
}

// Role is a role's word in the language: the glossary's, or the role as
// KIFF names it when the glossary has none.
func (m Membership) Role(role string) string {
	if w := m.RoleName[role]; w != "" {
		return w
	}
	return role
}
