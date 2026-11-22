# Email

Every email KIFF sends is drawn by one layout (`api/internal/notify/layout.go`) from the person's own words (`api/internal/words`). This is the guide for people who write or change one.

## What a KIFF email looks like

From **KIFF `<hello@kiff.dev>`**, reply-to `support@kiff.dev`. (Both are set in `api/cmd/api/main.go`; `KIFF_NOTIFY_FROM` and `KIFF_NOTIFY_REPLY_TO` override them.) The mail's own name is the brand: "KIFF", or "tafs by KIFF" for a sign-in through the tafs app.

One column, 560 px at most, tables and inline styles, no script, no web font, no tracking. **The header is the same on every email:** the KIFF mark (28 px, hosted on kiff.dev, `static/img/logo/kiff-logo-transparent-512.png`; tafs by KIFF has the tafs mark before it) next to the name, and **one amber rule** under it. The amber is kiff.dev's own (`--k-amber`, `#ff9a1a`); it is also the button to press, with dark text on it, never white. **The footer is mandatory on every email**, in this order: "KIFF will never ask for your password or code.", the Privacy and Help links (tafs by KIFF links the tafs Privacy page), and Kiff Agents OÜ with its registered address. In order, and each part is optional:

| Part | What it is |
|---|---|
| Brand | the logo(s) and the name |
| Lead | the sentence the email opens with: who did what, or what happened |
| Code | a sign-in code, large, monospace, always left to right |
| Paragraphs | the body, one thought each |
| Warning | one bold line (never share this code) |
| List | bullets (what a held action would do) |
| Details | a table of label and value (when, browser, place) |
| Note | a small line under the table |
| Buttons | links drawn as buttons; **the first is the one to press**, the rest are outlines |
| Small print | why you got this, what to do if it wasn't you |
| Footer | never-ask line, Privacy and Help, the company and its address: always |

Screenshots, in English, French and Arabic (`docs/email/sheet-en.png`, `sheet-fr.png`, `sheet-ar.png`). The Arabic sheet shows the three code sign-in emails, the only ones written in Arabic today; its dates are still English (see "Languages").

## Rules

- **Say "account".** Never "workspace" and never "KIFF Cloud": the product says account.
- **An email never answers for the person.** A button opens KIFF, where they sign in and decide. A link that answered by itself would let a mail scanner or anyone the email is forwarded to approve (see the held-action email).
- **No secret but the code.** A code goes in the code email and nowhere else, never in a subject. No token, no link that signs in, no password, no card number. A ticket's reply never travels by email, only a link to it.
- **Escape everything.** Every part is plain text and the layout escapes it. The one exception is `LeadHTML`, which the caller builds and escapes part by part (the held email's agent and action). A value an agent, a person or a provider chose (an account's name, a device, a browser, a place) is escaped and never reads as the sender.
- **Speak people, not code.** An email never says an identifier, a unit's name or a raw number: a call is "Refund a payment", an amount is "€47.00" in the person's currency format, and a date is in the person's zone and language. The held-action email takes these from the same view every app's Needs you shows (`handlers/held_email_words.go`), so they cannot differ; when KIFF can't read the hold it says what it can from the proposal.
- **Say what is known.** A detail that wasn't given has no row, never "unknown". A place from the network is called approximate.
- **Calm and short.** What happened, what to do, and if it wasn't you. No alarm, no marketing, no images except the logo.
- **No English in code.** The words are in `api/internal/words` in the person's language. A new email adds its lines there first.

## Languages

The language and direction are the person's: `lang` on the page, and `dir="rtl"` with right-aligned text for Arabic, Hebrew and Divehi. A row's value is isolated (`<bdi>`), so a Latin date or browser name in an Arabic line is not scrambled; the code stays left to right.

Today:

- the **code sign-in** emails are written in the eight languages of `words` and in 37 more (`words.CodeSignInEmailMore`);
- **every other email** (new device, invitations, account notices, held action, the Card budget warning) reads the person's language the same way: the eight in `words`, and the 37 more from **`words.EmailMore`**, which holds one `EmailLines` per language (a line per `Words` field the emails read, with `{0}`, `{1}` for the parts a sentence fills in). `words.Of(lang)` is English with a language's lines laid over it, so no email's code changes when a language is added. A language with no entry reads English, and so does one with a line missing: an incomplete entry is read as none (`words.EmailLinesProblems` names the missing line, an extra one, or a line whose parts are not the English one's, and a test fails on an incomplete entry), so an email is never half in a language and half in English. `words.EmailLanguagesMissing()` is the list still to write;
- **The page follows the words.** An email's language, its direction and its footer are those of the words it is really written in: `words.EmailLangFor` (new device, held action, invitation: the eight, or a complete `EmailMore` entry, else English), `words.AccountEmailLang` (suspended, reactivated, support) and `words.NoticeLangFor` (account notices: the eight, or a language with the emails' lines **and** the notices' sentences) and `words.CardLangFor` (Card warnings: the eight, or a language with the core **and** the cards' lines). So an Arabic reader gets English left to right until an email's words are written, never English laid out right to left. The table has two groups of lines: the **core** (every email drawn here) and the **notices** (28 sentences that handlers word before a notice reaches the layout: a new approver, a relinked phone, a pack or raise asked for, who joined, a Card changed, separation of duties off). Handlers word a notice from `words.OfNotice(lang)`, never `words.Of(lang)`: with the core and not the notices it is English throughout (a role's name and "an admin" are core lines, and must not sit inside an English sentence). A language is complete with the core alone; a half of the notices is reported by `EmailLinesProblems` and not used; the notices are used only with the core. `words.EmailNoticeLanguagesMissing()` is the list still to write. The third group, **cards** (26 lines), is the Card budget warning's sentence and a held action's reason line ("$45 is over the $40 limit for one call", "20 calls a day: this would be the 21st"), with what they are written with: a window's words, `DecimalComma`, `NoCurrency` and how an amount is written. It needs the core, and is used only whole: a language without it reads both sentences in English, whole, and its Card warning email is English throughout (`words.CardLangFor`); every app's held reason and the warning history use the same `words.Of(lang)`, so they follow. Two lines take a number and are written differently: **`CallCount`** and **`Ordinal`** hold one form per plural category of the language (CLDR: `zero`, `one`, `two`, `few`, `many`, `other`), like `one={0} call|other={0} calls`; the form for a number is the language's own rule (`golang.org/x/text/feature/plural`), `other` is the fallback, and `words.EmailPluralProblems(lang, lines)` names the category a language needs and lacks (Russian `few` and `many`, Arabic all six). **`Money`** is three lines, `Money.Thousands`, `Money.Gap` (between the amount and its currency) and `Money.CurrencyAfter`, whose separators are named (`none`, `comma`, `dot`, `space`, `nbsp`, `thin`, `apostrophe`) because a space that is a line's whole text cannot be told from no text. A line that takes a count (`OverCalls`) has it as `{0}`. `words.EmailCardLanguagesMissing()` is the list still to write.
- **suspended, reactivated and support-ticket update** (`words.AccountEmail`) and **the footer** (`words.EmailFooter`) are in `words.AccountEmails` and `words.EmailFooters`, by language tag; English until a language has them. An English footer is set left to right inside a right-to-left email;
- the person's language is kept as the emails speak it (`personlang`: the eight and the 37, from the app's Accept-Language), so an Arabic app's emails come in Arabic once `EmailMore["ar"]` is written.

To write a language: `KIFF_EMAIL_LINES=/tmp/en.json go test ./internal/words -run TestWriteEmailLinesEnglish` writes the English lines as templates; translate each value (keep every `{n}`), add the language to `EmailMore` in `words/email_more_<tag>.go`, its account and footer lines to `AccountEmails` and `EmailFooters`, and run `go test ./internal/words ./internal/notify`. The reason text a held-action email carries (why it is held) is still read in one of the eight (`replyLanguage`), English for the others.

## Adding or changing an email

1. Write the lines in `words` (English first; a test fails on a missing or empty line in the eight).
2. Build a `mail` and send it with `sendMail` (`ResendNotifier`). The layout adds the title (the subject), the language and direction, the footer and the escaping.
3. Write the plain-text part too: it says the same, in the same order.
4. Add the email to `TestEveryEmailIsOnTheLayoutAndSaysAccount` and to `TestWriteEmailSamples`.
5. Look at it: `KIFF_EMAIL_SAMPLES=/tmp/mail go test ./internal/notify -run TestWriteEmailSamples` writes one HTML file per email and language; open them in a browser, and in an Arabic one check that the direction is right.
6. Say in the PR which Docs rows it touches (the subprocessors list if it sends to a new kind of address; the Resend row names what is sent through it).
