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

**A new sign-in** that uses a verified email matching an existing user is that
same user, so it comes back to the same account. An address that doesn't match
(a relay address, say) is a new user, and so a new account.

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

A re-link answers `{"status": "relinked"}`, as the device flow does.

## 3. Consent

The welcome screen's terms tick goes with the token request as `terms_version`.
KIFF records `{subject, tenant, terms_version, client, device, time}`, with
KIFF's own clock, in the same step that mints the session. There's no separate
call to forget.

`GET /v1/auth/app/terms` answers `{"terms_version": "..."}`, the version in force.

The AI consent is not part of sign-in. It stays its own screen and its own
record.

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
| `terms_outdated` | 409 | `terms_version` isn't the one in force. Show the tick again. |
| `invalid_request` | 400 | A field is missing or malformed, or `/device/start` refuses a device field (same codes). |

The device flow's own refusals keep their codes: `proof_test_key` (a test key on
the live account), the rate limit, and a second device waiting for approval.

## Tests that hold this

Each PR adds its own tests. Before the switch goes on, the end-to-end test
covers: authorize, then token, then a signed request; and a reused code, a
wrong verifier, a wrong redirect, `state` echoed, `plain` refused, and the
switch off.
