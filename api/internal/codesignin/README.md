# Signing in with a code sent by email

A person can sign in with a six-digit code sent to their email, **if they turned it on**. It's off for everyone until they do.

**KIFF owns the setting, the codes and the emails. The sign-in provider still makes every session.** Once a code matches, KIFF asks the provider for a one-time sign-in token for that person. The page completes the provider's sign-in with it, and the usual callback mints KIFF's session. The session, sign-out, the new-device email and the apps' sign-in are all unchanged. The provider's own email-code sign-in stays off.

## The setting

`GET /v1/me/sign-in` and `PUT /v1/me/sign-in` with `{"email_code": true|false}`, for the person's own session.

```json
{ "email_code": false, "changed_at": "2026-11-22T13:05:00Z", "available": true }
```

- **It's per person** (the provider's user), whatever accounts they're in.
- **Turning it on needs a recent sign-in** (`auth.StepUp`, as approving does): `403 reauth_required` otherwise. That sign-in must not itself be a code sign-in.
- **A person with a second factor can't turn it on:** `409 second_factor`. A code never stands in for a second factor.
  - A device session (the apps' own) never passes the step-up check. So an app opens KIFF's Settings › Sign-in page in its sheet to turn codes on, and can turn them off itself.
  - **A phone's session** (one with a device key) reaches `GET` and `PUT /v1/me/sign-in` (they are in `auth.PhoneSessionRules`): it reads its own person's setting and turns codes **off**, and the person is told by email. A `PUT {"email_code": true}` from a device session is refused with `403 turn_on_at_kiff` ("turning on … is done at kiff.dev, in Settings › Sign-in, after a recent sign-in"). The setting is the session's own person's; it never names another.
- **Turning it off** needs nothing more, and drops any code still waiting.
- **Each change emails every verified address of the person**: "code sign-in was turned on / off".
- `available` is false when KIFF can't send codes (no `KIFF_SIGNIN_CODE_KEY`). Turning it on then answers `503 not_available`.

## Asking for a code and using it

These are the web's routes, from the box only. A call from anywhere else gets 404, and a call carrying a key or session gets 401. The web passes the client address that the region gate trusts.

- **`POST /v1/auth/code/send`** `{email, flow, ip, lang, browser, country}` → always `202 {"status":"sent_if_on"}`.
  - Every address gets the same work: its keyed hash, the counts, and one job queued. The answer goes before anything is looked up.
  - The job sends:
    - **a code** to a person with codes on, when the address is one of their **verified** addresses;
    - **"code sign-in isn't on, here's how to turn it on"** to a person with it off;
    - **nothing** to an address no one has verified.
- **`POST /v1/auth/code/verify`** `{email, flow, code, ip}` → `200 {"ticket"}`, `400 code_wrong`, `403 second_factor`, `429 slow_down` or `503`.
  - The code is matched against the browser's `flow` (the web's sealed cookie), so it works only in the browser that asked for it.
  - The ticket is for the person KIFF recorded with the code. Before minting it, KIFF checks again that the address is still that person's verified one and that codes are still on.
  - **A person with a second factor gets no ticket:** `403 second_factor`, even with the right code, in case they added one after turning codes on. The page tells them to sign in another way.

**A code:**
- six random digits;
- works once, for 10 minutes;
- kept only as an HMAC over its id, its person and the code, under its own key;
- compared in constant time.

**Limits:**

| what | limit |
|---|---|
| sends per address | 3 an hour, 10 a day |
| sends per IP address (IPv6 by /64) | 10 an hour |
| verifies per address | 10 an hour |
| verifies per IP address | 30 an hour |
| wrong codes per **person** | 5 an hour, 20 a day; a new code doesn't reset them, and past them every code of the person ends |

A send past a limit answers the same and sends nothing. The refusal is logged with an 8-character hash, never the address.

**What the limits trade:**
- **Wrong codes count per person,** so a stranger who knows someone's address can use up that person's tries and keep them from signing in with a code for an hour, or a day. They can still sign in any other way. Counting per browser instead would let one person's codes be guessed from many browsers, so the lockout is the price of that.
- **The "isn't on" email goes to anyone's verified address on request,** at most 3 an hour and 10 a day per address. It's the nuisance bound: no code is in it, and it says how to turn codes on, not that someone tried.

## A code sign-in is not a recent sign-in

KIFF notes each code sign-in (`signin_code_uses`) **before** it asks for the token.

For a quarter of an hour after one, `auth.StepUp` refuses any session of that person made after it, or one whose creation time it can't read. That covers keys, members, account changes, and turning codes on. An inbox alone never makes those changes.

**The one exception is the apps' sheet** (`appsignin`, `StepUp.WithoutCodeCheck`): approving the app's request *is* the sign-in. It passes only for the request the browser signed in for. The new-device email and the second-device rules apply, as for any sign-in.

## What is kept

| table | what | how long |
|---|---|---|
| `signin_settings` | person, on/off, when changed | while they have a sign-in; erased with it |
| `signin_codes` | keyed hashes of the flow and the address, the person, the code's HMAC | until used or 10 minutes |
| `signin_counts` | counts per keyed hash of an address, IP address or person | a day |
| `signin_code_uses` | person, when they last signed in with a code | a quarter of an hour |

A code, its HMAC, a token or an address in clear is never logged.

## Turning it on

1. **API:** `KIFF_SIGNIN_CODE_KEY`, 32 random bytes (hex or base64), used for nothing else. It needs Postgres, Resend and the sign-in provider. Unset, code sign-in is off and no one can turn it on.
2. **Web:** `KIFF_SIGNIN_CODE=on` shows the "Email me a code" link under both sign-in forms (`/sign-in`, `/sign-in/app`), and the pages at `/sign-in/code`. Off, neither the link nor the pages appear.
