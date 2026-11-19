# Connection profiles and Did this happen?

A **profile** describes an app the tafs app connected: what each operation does, where its amount is,
and how KIFF can tell it happened.
For whoever approves a connection, and for builders reading the API.
KIFF writes the words you approve for it, and applies it only with your phone's signature.

## The words are KIFF's

`POST /v1/profiles/words` takes a profile (`kiff.profile.v1`) and answers KIFF's words for it
(`kiff.words.v1`), such as "Refund a payment: moves money through Stripe." The app shows them as
they are. A profile never changes what a Card limits. With `?v=2`, it answers `kiff.words.v2`: the
same words, without lines about KIFF's own checks, such as a vendor it checks daily. An approval
of either version counts.

## What KIFF holds to

- **A Card's room comes back only when nothing happened is established.** A status code alone is
  never proof.
- **A lost answer stays counted**, and for a money call KIFF asks you **Did this happen?**
- **Amounts are exact.** More decimals than the unit takes is refused, never rounded.
- **What the app says back is checked.** On a difference, the operation is paused until you resume
  it (`POST /v1/connections/{name}/resume`).
- **New money operations start on probation:** you confirm the first three calls' outcomes.

Apps connected by API key: a refused, failed or unknown call stays counted, and an unknown one is
never sent again.

## Did this happen?

`GET /v1/me/did-this-happen` lists the money calls KIFF can't prove, each with the taf, action, app,
amount, what it acted on, when, why, and a link to the app's record when there is one.

Answer with `POST /v1/me/did-this-happen/{proposal}` `{"answer": "yes" | "no" | "not_sure"}`, signed
on the phone ([Device signatures](/docs/reference-device-signatures)):

- **Yes** keeps the call counted.
- **No** gives the Card its room back, with Face ID. Nothing is reversed at the app.
- **Not sure** keeps it with you for a day and opens Coach on it.

The first Yes or No stands.

See also: [Connect apps](/docs/tafs-connect-apps).
