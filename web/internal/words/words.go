// Package words holds the lines of the screens a tafs app opens in the
// browser, in the eight languages tafs ships: device approval
// (/auth/device) and a held call (/needs-you/{id}). A screen speaks the
// app's language (components.RequestLanguage). The terms follow the
// glossary apps/api keeps a copy of (api/internal/words/glossary.md),
// and a test checks them against it. Each language keeps its catalog's
// voice: du, vous, tú, tu, European Portuguese (formal), je, Ty.
//
// The rest of the web app is English; these screens are the ones a person
// reaches from the phone.
package words

import "fmt"

// TafsByKIFF is the brand a screen reached from the tafs app wears beside
// both logos. It's a name, the same in every language.
const TafsByKIFF = "tafs by KIFF"

// Device is device approval's lines. A field ending in F takes its values
// through the methods below.
type Device struct {
	Title, Eyebrow, ThisPhone string

	// A phone already signed in that asks to approve again with a new key.
	RelinkDoneTitle, RelinkDoneF string // the device
	RelinkTitleF, RelinkOnlyF    string // the device
	RelinkWhy                    string

	Code, Device, NewKey, Key string
	CheckCodeMatches          string
	CouldntApprove            string
	SignInAgain               string
	ApproveAgain, OtherCode   string

	ApprovedTitle, ApprovedNamedF, Approved string // ApprovedNamedF: the device

	ReviewTitle, ReviewNote, KeyNote string
	SignInTo, ApproveDevice          string

	EnterNote, CouldntContinue, DeviceCode, Continue string

	// The page's answers, after "Couldn't approve:" or "Couldn't continue:".
	ErrUnknown, ErrLookup, ErrEnterCode, ErrReauth, ErrApprove string
	NoticeCheck, NoticeChoose                                  string

	// RoleName is an account role's name ("approver"), as apps/api says it.
	RoleName map[string]string
}

func (d Device) RelinkDone(device string) string  { return fmt.Sprintf(d.RelinkDoneF, device) }
func (d Device) RelinkTitle(device string) string { return fmt.Sprintf(d.RelinkTitleF, device) }
func (d Device) RelinkOnly(device string) string  { return fmt.Sprintf(d.RelinkOnlyF, device) }
func (d Device) ApprovedNamed(device string) string {
	return fmt.Sprintf(d.ApprovedNamedF, device)
}

// Role is role's name in d's language, or role itself when it has none.
func (d Device) Role(role string) string {
	if n, ok := d.RoleName[role]; ok {
		return n
	}
	return role
}

// Languages are the languages these screens speak, English first.
var Languages = []string{"en", "de", "fr", "es", "it", "pt", "nl", "pl"}

// DeviceIn is device approval's lines in lang, English's for a language
// these screens don't speak.
func DeviceIn(lang string) Device {
	if d, ok := device[lang]; ok {
		return d
	}
	return device["en"]
}
