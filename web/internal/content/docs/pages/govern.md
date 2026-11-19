# Govern

Govern shows, for one action in a domain, the decisions KIFF made, their receipts and the posture.
For company accounts; a personal account sees the same decisions in Cards, Needs you and Activity.
It's the on-screen view of what a [proof pack](/docs/receipts-and-proof) exports.

## What it shows

- **Identity**: the action, entity type, environment and mode.
- **Posture**: `observe` or `enforce`, and when it last changed.
- **Decisions by outcome**, with a drill-down into each.
- **Receipts**, with their signature status.
- **Completeness**: whether the window is whole or truncated.

A section with nothing recorded says so; it never shows a staged success or demo data as yours.

## Observe vs enforce

- **`observe`**: KIFF records what it *would* have decided and withholds nothing. Start here.
- **`enforce`**: `allowed` runs, everything else is withheld by your runtime.

Posture is per domain; flip it from the domain's home page, and the flip is recorded. A runtime key
can't change the posture of the domain it runs under.

Govern shows what KIFF decided, not what ran downstream. See [What KIFF is](/docs/what-kiff-is).
