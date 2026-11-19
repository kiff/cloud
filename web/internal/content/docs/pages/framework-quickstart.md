# Build a domain with the framework

Build a runnable refund domain locally with the open-source KIFF framework (MIT, Go).
For builders who want to see a domain decide before using KIFF Cloud. No account, all on your machine.
To give an agent a Card without writing a domain, start with the [quickstart](/docs/quickstart).

## 1. Install and scaffold

```bash
go install github.com/kiff/kiff/cmd/kiff@latest
kiff new -scenario refund github.com/acme/refunds
cd refunds
go mod tidy
make demo
```

This generates an `Order` entity, a `MARK_PAID` action, an approval-gated `REFUND_ORDER` action, a
headless HTTP API and a demo script. Against a local framework checkout, add
`-replace-local /path/to/kiff`.

## 2. Read the decisions

```text
order-2  PAID
  agent → REFUND_ORDER  $12.00
  KIFF  ✓ ALLOWED → your executor refunds → REFUNDED

  agent → REFUND_ORDER  $999.00, high-risk
  KIFF  ⏸ HELD — approval required   (the executor did NOT run)
      human → approves
  KIFF  ✓ ALLOWED → REFUNDED

  agent → REFUND_ORDER  (same order, retried)
  KIFF  ✗ BLOCKED — order is already REFUNDED

  replay from events → REFUNDED
      materialized == replayed
```

KIFF returns the decision; **your code executes** on `allowed`. State rebuilds from the stored
events alone, so the record is proof you can check yourself.

The repeat refusal is something a unique constraint also does. What a per-call check can't do is
bound what an agent does in total: that is a [Card](/docs/kiff-cards), in KIFF Cloud.

## Next

- [How kiff-guard works](/docs/concepts)
- [Connect an agent](/docs/connect-an-agent)
- [Reusable operational domains](/docs/domains)
- The code: [`kiff/kiff`](https://github.com/kiff/kiff)
