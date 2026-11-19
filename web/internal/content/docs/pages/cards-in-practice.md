# Cards in practice

Five short examples of a [KIFF Card](/docs/kiff-cards) at work.
For owners deciding what Card to give an agent.
Find the case closest to yours and copy its shape.

## A refund agent with a daily budget

```text
refund-agent may call refund_order
up to 100 EUR a day, at most 20 EUR a call
past that: Ask (hold it for an approver, 10 minutes)
```

A 15 EUR refund is sent. A 25 EUR refund waits on **Needs you** until
the owner answers. The tool's answer is recorded beside the approval.

![Needs you, Answered: a 25 EUR refund_order approved once, and the tool reported it done.](/static/img/docs/needs-you-answered.jpg)

## A number of plan changes a day, refused past it

```text
billing-agent may CHANGE_PLAN
up to 40 plan changes a day
past that: Stop (refuse it)
```

The 41st change today is refused with `mandate_limit_reached`. Nobody is
asked. A count limit counts attempts, so a change the billing system
rejects still counts.

## Start a new agent small

Give a new model its own key and its own Card at a low limit, such as
50 EUR a day. Read its statement, then raise the limit with **Limits**
on the Card.

## Two agents, one budget

Open **Share** on the Card and name the second agent. Both draw on the
same totals. **Stop sharing with** removes one holder.

## Stop one agent now

- Revoke its gateway keys and sign-ins on **Connections**, and its
  kiff-guard keys on its **Keys** tab. Its Cards and history stay.
- Or revoke its Card, if only this agent holds it.
