# App sign-in: the contract

The tafs apps (iPhone, iPad, Mac, Android) sign a person in **inside the app**,
in the system's own browser sheet, with the authorization code flow and PKCE
(RFC 7636, RFC 8252). Once the code is exchanged, everything is the device
flow's (`handlers/device_auth.go`): the same session, the same device record
with the phone's signing and approval keys, the same new-device email, and the
same rules for a second device. Nothing after sign-in changes for the apps.

**Off by default.** Behind `KIFF_APP_SIGNIN`. While it's off, `tafs-app` is an
unknown client (`unsupported_client`), and the device flow is untouched.

## 1. Authorize

```
GET https://api.kiff.dev/oauth/authorize
  ?response_type=code
  &client_id=tafs-app
  &redirect_uri=dev.kiff.tafs:/oauth
  &code_challenge=<BASE64URL(SHA256(verifier))>
  &code_challenge_method=S256
  &state=<random, at least 128 bits>
  [&ui_locales=en|de|fr|es|it|pt|nl|pl]
  [&login_hint=<email>]
```

- **`client_id=tafs-app`** is first party: there is no consent page. All the
  apps are one client.
- **`redirect_uri`** must be exactly `dev.kiff.tafs:/oauth`. That's a
  private-use scheme (RFC 8252 §7.1), which PKCE makes safe. App Links or
  universal links can be added later as a second exact value.
- **PKCE is required**: `code_challenge_method` must be `S256`. `plain`, or no
  challenge, is `invalid_request`.
- **`state` is required.** The app checks it on the way back.
- **`ui_locales`** sets the sign-in page's language. Anything else falls back to
  English.
- **`login_hint`** only prefills the email. It never signs anyone in.
- **No `scope`.** The app's session is the device flow's, and agents' scopes
  don't apply to it.

**With no session in the sheet**, the sign-in page opens in it and offers the
sign-in options KIFF has on. Once signed in, KIFF makes the account if it's the
person's first sign-in (`tenant.EnsureForSubject`), then redirects:

```
dev.kiff.tafs:/oauth?code=<code>&state=<state>
```

**The code** lives 60 seconds, works once, and is bound to the challenge, the
redirect, the client and the person.

**The person acts in the sheet, every time.** Any other app on the device can
register `dev.kiff.tafs:` and start its *own* authorize with its *own* PKCE
pair, so PKCE alone doesn't stop it. What stops it is that KIFF never issues a
code without the person acting in this sheet, for this request:
- No silent sign-in and no automatic continue. A session already in the
  browser, KIFF's or the sign-in service's, never completes an authorize by
  itself.
- The sign-in page proves it showed the person the form, or a "Continue as …"
  screen they tapped, for this request id. It does that with a short-lived
  HttpOnly cookie bound to the request and the session, used once and checked
  server-side before it calls approve.
- Approve also needs a recent sign-in, as below.
- The end-to-end test runs a second authorize with the first one's cookies
  present, and expects no code until the person acts.

**Claimed https links** (App Links and universal links), which no other app can
register, are decided **before the switch goes on in production**, not later.

**`login_hint` sends nothing.** It prefills the email field, escaped as text,
and no email code is sent until the person taps to send it. An authorize URL
can never send a code to an address.

**Approving needs a recent sign-in**, as the device flow's approve does: the
code mints a session that can approve held calls. A sheet that finds an older
session asks the person to sign in again, then comes back.

**A new sign-in** that uses a verified email matching an existing user is that
same user, so it comes back to the same account. The sign-in service does this
linking, not KIFF, and KIFF relies on it only for emails the provider marks
verified: a provider's `email_verified`, or KIFF's own email code. An
unverified address never lands in an existing account, and the end-to-end test
proves it. An address that doesn't match (a relay address, say) is a new user,
and so a new account.

## 2. Token

```
POST https://api.kiff.dev/v1/auth/app/token
Content-Type: application/json

{
  "client_id": "tafs-app",
  "code": "...",
  "code_verifier": "...",
  "redirect_uri": "dev.kiff.tafs:/oauth",
  "terms_version": "2026-11-20",

  "public_key": "...",
  "approval_public_key": "...",
  "device_name": "...",
  "platform": "...",
  "kind": "...",
  "approval": "...",
  "key_store": "..."
}
```

The second group is exactly what `POST /v1/auth/device/start` takes today, with
the same meaning and the same checks.

This is a separate route from agents' `POST /oauth/token`, so an app's session
and an agent's grant never mix.

**The answer** is the device flow's `DeviceToken`, unchanged:

```json
{ "access_token": "...", "token_type": "Bearer", "expires_in": 0,
  "tenant": "...", "subject": "...", "approval_mode": "..." }
```

A re-link (a phone's new approval key) stays on the device flow
(`POST /v1/me/device/relink`); this route never answers `relinked`.

**There is no waiting.** In the device flow the phone polls
(`authorization_pending`) only until the person approves in a browser. Here,
signing in inside the sheet is that approval, so this call answers at once.

**A second device** gets the device flow's rules, unchanged:
- A phone or tablet that sends an approval key enrols it, and the person gets
  the new-device email.
- A computer gets `"approval_mode": "view"`, then pairs with a device that
  approves, as today.

`approval_mode` in the answer says which.

## 3. Consent

The welcome screen's terms tick goes with the token request as `terms_version`.
KIFF records `{subject, tenant, terms_version, client, device, time}`, with
KIFF's own clock, in the same step that mints the session. There's no separate
call to forget.

`GET /v1/auth/app/terms` answers `{"terms_version": "..."}`, the version in force.

The AI consent is not part of sign-in. It stays its own screen and its own
record.

**What the app can fix leaves the code alone.** Old terms (`terms_outdated`),
a missing version, or a device field the device flow refuses is answered
before the code is spent, so the app retries with the fix within the code's
minute and never reopens the sheet. Anything about the code itself (a wrong
verifier, redirect or client) spends it.

**A replay ends what the code made.** A code that's used again after it minted
a session revokes that session, and the device it made (RFC 6749 §4.1.2). The
second request gets `invalid_grant`.

**Limits.** Failed token requests are limited per IP. Email codes are the
sign-in service's, limited per address and per IP, and its "send a code"
answers the same whether or not the address has an account. KIFF's codes are
kept only as hashes, and the verifier is compared in constant time.

**Nothing secret in logs.** No `code`, `code_verifier`, email code or
`access_token` goes into logs, metrics or `error_description`, and
`error_description` is fixed text that never echoes the request.

## 4. Errors

**On the redirect**: `error` and `state`, with an optional `error_description`
in English for logs only. The app words each code itself.

| `error` | When |
|---|---|
| `access_denied` | The person closed the sheet or declined. |
| `invalid_request` | A parameter is missing or wrong (PKCE, `state`, `response_type`). |
| `unsupported_client` | App sign-in is off. |
| `temporarily_unavailable` | KIFF couldn't finish just then. Try again. |

A wrong `client_id` or `redirect_uri` is never redirected, as RFC 6749 §4.1.2.1
requires: it's a 400 page in the sheet.

**On token**: `{"error": "...", "error_description": "..."}`.

| `error` | Status | When |
|---|---|---|
| `invalid_grant` | 400 | The code expired or was used already, or the verifier, redirect or client doesn't match. |
| `terms_outdated` | 409 | `terms_version` isn't the one in force (the answer names it). Show the tick again, then retry with the same code. |
| `unsupported_client` | 400 | App sign-in is off. Never a bare 404. |
| `slow_down` | 429 | Too many failed token requests from this address. Wait, then start again. |
| `temporarily_unavailable` | 500 | The sign-in couldn't be recorded. Nothing was kept; start again. |
| `invalid_request` | 400 | A field is missing or malformed, or `/device/start` refuses a device field (same codes). |

The device flow's own refusals keep their codes: `proof_test_key` (a test key on
the live account), the rate limit, and a second device waiting for approval.

## Tests that hold this

Each PR adds its own tests. Before the switch goes on, the end-to-end test
covers:
- authorize, then token, then a signed request;
- a reused code (the first session revoked), a wrong verifier, redirect or
  client (the code spent), `state` echoed, `plain` refused, and the switch
  off (`unsupported_client` on both routes);
- a second authorize with the first's cookies present: no code without the
  person acting;
- an unverified email that matches an existing user: not that user's account;
- `login_hint` sending no code by itself.
