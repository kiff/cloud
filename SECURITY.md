# Security policy

## Report privately

Report a suspected vulnerability using
[GitHub private vulnerability reporting](https://github.com/kiff/cloud/security/advisories/new)
or email [security@kiff.dev](mailto:security@kiff.dev), the contact published in
[KIFF's security policy](https://kiff.dev/security).

Do not put exploit details, credentials, customer data, or private infrastructure
information in public issues or pull requests. Use public issues for
non-sensitive bugs.

Include the commit named in `SOURCE`, the impact, prerequisites, and a minimal
reproduction using synthetic data. Explain expected and observed behavior.
Share redacted logs only; never send live tokens, passwords, or signing keys.
Test only on systems and accounts you own or have permission to assess.

## Scope

Relevant boundaries include: a call reaching a tool without an allowed
decision, or more than once; a permit accepted for other arguments, another
tool or after it expires; a halt that does not stop calls; a receipt that
verifies after it was changed; one account reaching another's connections,
tools, decisions or receipts; tool credentials leaving the gateway; the gateway
reaching internal addresses.

Repository visibility is not permission to test against the hosted service
(`kiff.dev`, `app.kiff.dev`, `api.kiff.dev`, `mcp.kiff.dev`) or another
person's account.

## Maintenance and disclosure

This repository is an export of the code KIFF Cloud runs; `SOURCE` names the
commit it came from. No older release support schedule is published.

Maintainers acknowledge the report privately, reproduce and assess it,
coordinate the fix and its rollout, and agree disclosure with the reporter.
Sensitive fixes stay private until they are deployed. An advisory is published
when disclosure is appropriate.
