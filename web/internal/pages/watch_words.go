package pages

import (
	"context"
	"io"
	"strings"

	"github.com/a-h/templ"
	"github.com/kiff/cloud/web/internal/components"
)

// watchWords are /tafs/watch's words in one language. English is watchEN;
// each other language has its own watch_words_<lang>.go, a translation held
// to the English by TestTranslationsKeepTheEnglishPage and
// TestEveryLanguageFillsTheWatchPage. Product, store and device names stay as
// they are. What the watch itself shows is quoted as the tafs app words it,
// in that language: the page names what the person sees.
type watchWords struct {
	Lang string
	// Title and Description are the page's head.
	Title, Description string
	// Badge, Head and Sub open the page.
	Badge, Head, Sub string
	// The sections, in the page's order (watch.templ gives each its anchor).
	Does, Get, Act, Coach, Locked, Keeps, Help watchSection
}

// watchSection is one numbered section: its heading and its list. An item
// is plain text in which **these words** are bold.
type watchSection struct {
	Head  string
	Items []string
	// Link, when the section has one, is the list's last item, with a link
	// in it.
	Link watchLink
}

// watchLink is a list item with a link: Before, the link named Text, After.
// Before may hold **bold** words too. Where it links is the template's
// (watch.templ), the same page in every language.
type watchLink struct {
	Before, Text, After string
}

var watchEN = watchWords{
	Lang:        "en",
	Title:       "tafs on your watch — KIFF",
	Description: "See what needs you, refuse a held call, halt a taf and ask Coach from your watch. Approving stays on your phone.",
	Badge:       "tafs · watch",
	Head:        "tafs on your watch",
	Sub:         "See what needs you, refuse a held call, halt a taf and ask Coach, from your wrist. Approving stays on your phone.",
	Does: watchSection{
		Head: "1. What it does",
		Items: []string{
			"**Needs you:** how many held calls need you, and each one as your phone shows it: its taf, what it wants, the amount and why.",
			"**Your tafs:** each taf's state in one line.",
			"**Refuse** a held call, **Halt now** one taf, or **Halt all now**, which the watch asks you to confirm once more.",
			"**Ask Coach** by voice.",
			"**A tile** with how many need you.",
			"**Approving is never done on the watch.** For a held call, a Card, a payment or a connection, the watch says “Approve on your phone” and opens it there.",
		},
	},
	Get: watchSection{
		Head: "2. Get it",
		Items: []string{
			"**Wear OS: Coming to Google Play.** Wear OS 3 or later. The watch app comes with tafs for Android: once it's on Google Play, install it from Google Play on your watch.",
			"**Apple Watch: Coming.** It will come with tafs for iPhone and do the same things, with approving on your iPhone.",
			"**No sign-in on the watch.** It uses tafs on your phone, which has to be signed in and nearby.",
		},
	},
	Act: watchSection{
		Head: "3. Refuse and Halt",
		Items: []string{
			"**Your watch needs a screen lock, and has to be unlocked.** Otherwise it says: “To refuse or halt from this watch, set a screen lock on it and unlock it.”",
			"**Your phone does it,** with its own key, and the watch shows the result once it's done, such as “Refused. Nothing was sent.”",
			"If someone answered the call in the meantime, the watch says “Already answered, or it ended.”",
		},
	},
	Coach: watchSection{
		Head: "4. Ask Coach",
		Items: []string{
			"Tap **Ask Coach** and speak. Your watch's own speech input turns what you say into text, and the watch shows it before anything is sent: **Send** or **Cancel**.",
			"**Coach gets your words as text, never your voice.**",
			"First, allow AI use in tafs on your phone, and start Coach there.",
			"A short answer is shown on the watch. A long one, or one with something to answer, shows its start and **Open on phone**.",
			"As for Refuse and Halt, your watch needs a screen lock and has to be unlocked.",
		},
	},
	Locked: watchSection{
		Head: "5. When your watch is locked",
		Items: []string{
			"The tile shows only “tafs”, and an alert shows only “A taf needs you”. Wear OS locks your watch when you take it off your wrist.",
			"Unlocked, an alert shows what your phone's does: which taf and what it wants, which can name an amount, a merchant or a person.",
			"On an Apple Watch, alerts are your iPhone's, so your iPhone's preview setting decides what they show. With previews hidden, which is the iPhone's setting when it's locked unless you change it, an alert shows only “A taf needs you”.",
		},
	},
	Keeps: watchSection{
		Head: "6. What it keeps",
		Items: []string{
			"Only how many need you, for the tile. It's never shown while your watch is locked, and it's cleared then, or when you sign out on your phone.",
			"No sign-in, no keys. What it shows is forgotten when you put it away, and nothing you say is kept.",
		},
		Link: watchLink{Before: "The details are in ", Text: "tafs's privacy policy", After: "."},
	},
	Help: watchSection{
		Head: "7. If something isn't right",
		Items: []string{
			"“Your phone isn't in reach. Bring it closer, then try again.”",
			"“Sign in to tafs on your phone first.”",
		},
		Link: watchLink{Before: "Anything else: **Open on phone**, or ", Text: "write to us", After: "."},
	},
}

// words is the page's words: English unless a language was given.
func (w watchWords) words() watchWords {
	if w.Lang == "" {
		return watchEN
	}
	return w
}

// href is a translated page's path in the page's language.
func (w watchWords) href(path string) templ.SafeURL {
	return templ.SafeURL(components.LangPath(w.words().Lang, path))
}

// watchRich writes s with each **span** in bold; everything else is text.
func watchRich(s string) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, out io.Writer) error {
		for i, part := range strings.Split(s, "**") {
			if part == "" {
				continue
			}
			text := templ.EscapeString(part)
			if i%2 == 1 {
				text = "<strong>" + text + "</strong>"
			}
			if _, err := io.WriteString(out, text); err != nil {
				return err
			}
		}
		return nil
	})
}
