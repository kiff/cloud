# tafs: how it works

tafs is an app for iPhone, iPad and Android that runs routine jobs with AI teammates called **tafs**.
It's for a person or a team handing over work such as refunds, disputes or overdue invoices.
Each taf acts only through KIFF, under a [Card](/docs/kiff-cards) you issue it with Face ID.

tafs is on Google Play in Europe, the UK and Switzerland, and on TestFlight for iPhone and iPad.
Where this page says Face ID, Android asks for your fingerprint. Plans: [/pricing](/pricing).

![The tafs list with sample data: three tafs working and two that need you, each with its limit and what it has used today.](/static/img/tafs/tafs-list.jpg)

## A taf and its Card

A **taf** is one AI teammate with one job. It reaches your tools only through the
[KIFF MCP gateway](/docs/mcp-gateway) and never holds your sign-ins.

Its **Card** says what it may do, how much and until when. KIFF checks every call first: over a
limit is held for you, refused by the Card is refused. See [Held calls and approvals](/docs/approvals).

## Needs you

A held call waits in **Needs you**, with the rule that held it and the time left. Approve once
with Face ID, pick another option the taf offered, or refuse. Unanswered, it's refused when the hold expires.

If approving once can't pass a day's or week's total, the hold offers **Extra room**: just the room
that call needs until the total resets. A money call KIFF can't prove asks
[Did this happen?](/docs/tafs-connection-profiles).

## Coach mode

**Coach mode** is where tafs are made, connected and changed. Tell it the job in your own words; it
drafts the taf and its Card and opens each vendor's sign-in page. Nothing starts until you issue the
Card with Face ID. Coach also answers why a call was held or refused.

## The app's screens

The bar: **Account · Tafs · Needs you · Record**, plus **Coach** for whoever may use it. The
**Record** lists everything your tafs did, asked or were refused, by day.

Each taf has four tabs:

- **Chat**: talk to it, or tell it to work differently.
- **Activity**: what waits for you, then what it did, filtered All, Done, Held or Refused.
- **Card**: what's on its Card, with Halt and Revoke.
- **Setup**: the job, what starts it (schedule, app events, [doors](/docs/tafs-doors)), connected
  apps, where results go, what it remembers, its versions, and Remove.

An app event wakes nothing until you approve its filters with Face ID. A change to Setup waits as
the next version, **not live**, until you make it live. See [Try a version](/docs/tafs-try-a-version).

## Notes and shared tafs

A taf keeps **notes** on how you like the job done. They shape how it works, never what its Card
allows. **Forget** removes one.

Each person's chats with a taf are their own: no one else on the account reads them, not even an
admin. **Shared notes** are read by everyone who uses the taf; your own notes only in your chats.
Scheduled runs and doors read the shared notes only.

## More

- [Connect apps](/docs/tafs-connect-apps)
- [Approving on your phone](/docs/tafs-approving-on-your-phone)
- [Who can do what](/docs/tafs-roles)
- [Halt and resume](/docs/tafs-halt)
- [Credits and your balance](/docs/tafs-credits)
- [Where data lives](https://github.com/kiff/tafs/blob/main/docs/data.md)
