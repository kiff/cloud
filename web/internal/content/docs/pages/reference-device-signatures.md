# Device signatures

How a phone or computer signs its answers to [held calls](/docs/approvals) with a key only that device can use.
For developers of an app that answers held calls or issues Cards.
Bind a P-256 key at sign-in, then sign each answer and grant; a stolen sign-in token then approves nothing.

The key lives in the device's secure hardware (Secure Enclave, Android
Keystore, a Windows TPM). A device signed in without a key can read held
calls but not answer them (`device_required`). In the tafs app, see
[Approving on your phone](/docs/tafs-approving-on-your-phone).

## 1. Binding the key at sign-in

Start the device sign-in (`POST /v1/auth/device/start`) with
`public_key` (P-256, base64 SPKI DER), `device_name` and `platform`
(`ios`, `ipados`, `macos`, `android`, `windows` or `linux`), and optionally
`kind` (`iphone`, `ipad`, `android_phone`, `android_tablet` or `other`).
Approving the sign-in in the browser binds the key to that session.

## 2. Signing an answer

Approve once, reject and change Card need
`"signature": {"signed_at", "value"}` in the body. `value` is a base64
DER ECDSA P-256 signature over the SHA-256 of these lines:

```
kiff.answer.v1
<tenant_id>
<exception_id>
<answer>                approve-once, reject or change-card
<next_step>             empty unless a refusal names one
<sha256 of the note>    hex; empty when there is no note
<accounting_version>    the hold's, from GET /v1/me/card-exceptions/{id}
<signed_at>             RFC 3339, the same string as in the body
```

Refusals: `signature_required`, `signature_invalid`, and
`signed_at_out_of_range` (more than 5 minutes from KIFF's clock). The
receipt keeps the signed text and signature, so anyone can check it.

**A held call that fills a sign-in saved in the owner's Chrome** (its
arguments name a sealed handle, `"sealed": "sec_…:password"`) is allowed only
by `approve-once` carrying the phone's release beside its signature:
`"release": {"chrome_id", "op", "origin", "nonce", "exp", "sig"}`, the
phone's approval key over `tafs-chrome/release/v1`, which the owner's Chrome
checks before it fills. KIFF refuses an allow without one, from any client
(`release_required`, 400: also a release with a missing field, an origin
that isn't https, a nonce under 16 bytes, or an `exp` past or more than 2
minutes ahead), and never lets one go by a lasting answer: `change-card` and
an approver's approve are `sealed_fill_once` (409), and "Allow this from now
on" offers no change. Refusing it needs nothing more. A hold whose call
KIFF can't read to check is answered 503, and stays held.

`change-card` lasts when a per-action limit or resource scope was in the
way; for a reached total it adds room for this period only. A call that
hit both is held again after the first answer.

## 3. What a phone's session may do

Read Cards and held calls, answer, revoke a Card, read receipts, list
agent drafts, and list or sign out devices. Anything else is refused
with `phone_session_forbidden`.

## 4. Signed requests

Creating an agent, issuing a Card, minting a key and giving extra room
need `KIFF-Device-Signature: t=<signed_at>,v1=<signature>` from a phone,
over the SHA-256 of:

```
kiff.request.v1
<tenant_id>
<METHOD>
<path and query, as sent>
<sha256 of the body>    hex
<signed_at>             RFC 3339, the same string as in the header
```

Refusals: `signature_required`, `signed_at_out_of_range`,
`signature_replayed`. A phone can mint only keys bound to an agent.

## 5. The approval key

An approval key needs Face ID or the passcode before it signs. Send it at
sign-in as `approval_public_key`, or register it once with
`POST /v1/me/device/approval-key`, signed by the device key. Once set,
approve once, change Card, issuing a Card, extra room, draft submit and
confirm, and resume count only with its signature
(`approval_key_required` otherwise).

## 6. The approval window

`POST /v1/me/window`, signed by the approval key, opens a window of up
to 5 minutes (capped at 15) for routine actions: a new Card, a narrower
revision, and a draft's submit and confirm. Inside it, sign with the
device key as `t=…,v2=…,w=<window id>`. Widening a Card, extra room,
resume and sharing are never routine (`critical_key_required`).

## 7. Held-call alerts for a phone app

A signed `POST /v1/me/alert-targets` registers the app's backend, if
KIFF lists it for phone apps, as the account's alert target for
`owner_required` and `hold_answered`. See [Webhooks](/docs/reference-webhooks).

## 8. Passkeys and security keys

A computer with no secure key store of its own, such as an older Mac or
Linux, approves with a **passkey**: the phone's own passkey through a QR
code, or a FIDO2 security key. It sends an assertion in place of a device
signature:

```
KIFF-Passkey-Assertion: t=<signed_at>,a=<assertion>
```

`a` is the base64url of `{"credential_id", "client_data_json",
"authenticator_data", "signature"}`. The assertion's challenge **is** the
action's digest: the base64url of the SHA-256 of the same `kiff.request.v1`
message a device key signs, with the same `signed_at`. So a passkey approves
exactly one action, as a device signature does, and each challenge is used
once.

KIFF accepts an assertion only when every check below holds:

- `clientDataJSON.type` is `webauthn.get`;
- the origin is `https://kiff.dev`, never inside another site's frame (`crossOrigin`), and `rpIdHash` is the SHA-256 of `kiff.dev`;
- the challenge equals the action's digest, byte for byte;
- the user was present and verified (the UP and UV flags);
- the signature verifies over `authenticatorData ‖ sha256(clientDataJSON)` with the passkey's registered P-256 key;
- the sign counter never goes backwards (an authenticator that keeps no counter reports 0 every time);
- the passkey is registered to this account and this person, and a computer's own passkey to this computer, never synced.

**An account's passkeys.** `POST /v1/me/passkeys` registers one, signed by
an existing approval device's approval key (never a passkey, a window or a
device key alone), by an owner or admin. `GET /v1/me/passkeys` lists them;
`DELETE /v1/me/passkeys/{id}` removes one, itself an approval. A passkey
synced to the computer's own cloud account never counts as the second
device that payments need.

**A computer's own security key.** A computer whose device key is in
software pairs (section 9) with `kind: "passkey"`, `key_store: "software"`,
its security key's `credential` and `credential_proof`. That key alone then
approves for it, with its PIN every time (UV), one request at a time.
Refusals: `passkey_required` (a device key's signature alone),
`user_verification_required` (no UV), `passkey_not_this_device` (an account
passkey), `security_key_each_request` (two requests at once, or a window),
and `security_key_only` (registering an approval key, which would be in
software too). Without a security key, it pairs to view only.

## 9. Pairing a computer from a phone

A computer shows its keys, a nonce and an expiry as a QR code. The phone
sends the QR's JSON to `POST /v1/me/pairings`, signed by its approval key
(a window never covers it), and gets `201 {"pairing": {"id", "code",
"expires"}}`. Refusals carry `words` for the person: `invalid_pairing`,
`pairing_expired`, `pairing_used`, `key_registered`.

The computer polls `POST /v1/pair/claim` with no auth, every 3 seconds or
more, with `{"nonce", "pub", "proof"}`: `proof` is its device key's
signature over `kiff.pair.claim.v1`, the nonce and the SHA-256 of `pub`.
It answers `authorization_pending`, `slow_down`, `expired_token` or
`invalid_proof`, then, once, a session bound to the keys the phone
approved, with its `device_id`.

## Your devices

`GET /v1/me/devices` lists them, each with its `kind` and a
`display_name` drawn from it in the request's `Accept-Language` (`name`
is what the device sent; `display_name` is it for kind `other`);
`DELETE /v1/me/devices/{id}` removes one and ends its sign-ins.
