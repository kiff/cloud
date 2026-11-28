# K4: universal classes and sign-in identity (contract, for review before code)

**Rule (owner):** nothing per vendor but guides, words and listings. A tool's class comes from what the vendor declares about
it, by one rule for every app. A reviewed per-app table may only raise a class (an override), never be needed to keep a tool
safe. A resumed sign-in needs the vendor to say whose account it is, by one rule for every app.

## Part 1: classes from what the vendor declares (KIFF's gateway)

A tool's class is the worst any of these gives, read in order. Every signal may **raise** a class alone. A class can be
**lowered** below `irreversible` only when two independent signals agree.

1. **The vendor's MCP annotations**, as planque relays them: `readOnlyHint`, `destructiveHint`.
   - `destructiveHint: true` raises to at least `irreversible` (`delete` for a delete verb). It already does today.
   - `readOnlyHint: true` is one signal toward `read`, and never enough alone (step 5).
2. **The scope the tool needs**, when the vendor declares it per tool (an MCP tool's `_meta` security requirement, or an
   OpenAPI operation's `security` scopes, as planque relays them).
   - A scope whose action is `write`, `transfer`, `payment`, `card` or `admin` raises the class (`money` for transfer,
     payment and card scopes; `irreversible` for admin, membership and team writes).
   - A scope that only reads (`*.read`, `read:*`, `*:read`) is one signal toward `read`.
   - Not declared: no signal either way.
3. **The verbs and words in the tool's name**, as today (`class.go`): delete verbs give `delete`; send, share, export, forward,
   finalize and the rest give `irreversible`; money words give `money`. A read verb (`get_`, `list_`, `search_`) is one signal
   toward `read`.
4. **The connection's money mark** (a profile's `money: true`): `money`, as today.
5. **Lowering needs two:** a tool is `read` only when **two** of {`readOnlyHint: true`, a read-only scope, a read verb} agree,
   and nothing in 1–4 raised it. It's `write` only when its name has a verb this list knows (create, update, add, set, move,
   upload and the like) and nothing raised it.
6. **Unknown is closed:** a tool nothing above classes (no annotation, no declared scope, a name with no known verb) is
   `irreversible`, closed on the Card until the owner opens it. Today the same tool is a `write`, which is open.

**Per-app tables (`classes/<app>.json`) become overrides only.** They may only raise, as `Classify` already enforces
(`worse`). The test that every table tool classes at least as strictly by the rules alone stays, and so does the test that a
table never lowers. A tool missing from every table is still classed by the rule, never left open.

**What changes for live accounts:**
- Tools that were `read` on one signal (planque's read_only plus a read verb) stay `read`, since that's two signals.
- Tools that were `write` only because nothing matched become `irreversible` and closed. **Each such tool on a live Card is
  listed before the switch**, and the owner's Cards keep opening what they opened by name: a Card that names a tool opens it
  whatever its class.
- Behind `KIFF_K4_UNIVERSAL` per account, as K4 itself was, read back from the running API before an account is switched.

### Worked examples

| App | Tool | Signals | Class |
|---|---|---|---|
| Qonto | `list_client_invoices` | readOnlyHint, scope `client_invoices.read`, verb `list_` | `read` (three agree) |
| Qonto | `get_organization` | read_only (planque), verb `get_`, scope `organization.read` | `read` |
| Qonto | `create_client` | verb `create_`, scope `client.write` | `write` |
| Qonto | `create_client_invoice` | verb `create_` + `invoice`, scope `client_invoice.write` | `irreversible` (the draft pin lowers a call with `status: draft` to `write`, as today) |
| Qonto | `send_client_invoice` | verb `send_` | `irreversible` |
| Qonto | `create_transfer_request` | scope `request_transfers.write`, word `transfer` | `money` |
| Qonto | a SEPA direct-debit tool (`sepa_direct_debit.write`) | scope: payment | `money` |
| Qonto | a membership or team write (`membership.write`, `team.write`) | scope: admin write | `irreversible` |
| Qonto | a bank-account write (`bank_account.write`) | scope: account write | `irreversible` (`money` if it moves funds) |
| Qonto | `show_view` | no annotation, no scope, no known verb | **`irreversible`, closed** (today `write`; the table may keep it `write` only by raising nothing, so it moves to closed unless a second signal says read) |
| Stripe | `stripe_api_write` pinned to `PostInvoicesInvoiceVoid` | operation verb `void` | `irreversible` |
| Stripe | `list_customers` | readOnlyHint, verb `list_` | `read` |
| Composio | `GMAIL_SEND_EMAIL` | app prefix dropped, verb `send_` | `irreversible` |
| Composio | `GMAIL_LIST_THREADS` | readOnlyHint, verb `list_` after the prefix | `read` |
| Composio | `NOTION_DO_THING` (made up) | no annotation, no scope, no known verb | `irreversible`, closed |

**Qonto's 32 granted scopes under this rule:**
- reads (`*.read`): `attachment.read`, `card.read`, `cash_flow_category.read`, `client.read`, `client_invoices.read`,
  `einvoicing.read`, `insurance_contract.read`, `membership.read`, `organization.read`, `payment_link.read`,
  `product.read`, `sepa_direct_debit.read`, `subscription.read`, `supplier_invoice.read`, `team.read`. Each gives a tool's
  `read` only with a second signal (its read verb or readOnlyHint).
- money: `card.write`, `request_cards.write`, `request_transfers.write`, `payment_link.write`, `sepa_direct_debit.write`.
- irreversible: `membership.write`, `team.write`, `bank_account.write`, `insurance_contract.write`,
  `request_review.write` (approves others' requests).
- writes, raised further by verb or word where they apply: `attachment.write`, `cash_flow_category.write`, `client.write`,
  `client_invoice.write`, `product.write`, `supplier_invoice.write`.
- `offline_access`: no class; it's how the sign-in lasts.

**The one edge for the security review:** can `readOnlyHint` alone make a tool `read`? This contract says no: a hint is one
signal, and lowering needs two.

## Part 2: a generic identity read at every callback (planque)

At each OAuth callback, planque reads whose account the sign-in is, by one rule:
1. the provider's OpenID userinfo (`sub`, `email`, `name`), or token introspection, where offered;
2. else the MCP server's own identity tool, if it declares one (a read-only tool whose description says it returns the
   signed-in account, read once at the callback);
3. else **empty**.

- What's kept is `account: {id, label}`: an id the vendor gives and a label for the owner. No token, no other field.
- **Empty means no resume** for that app: a sign-in that can't say whose account it is can't be resumed on another device
  or after a lapse, and fails closed to a fresh sign-in.
- The owner sees the label in the words planque already sends (`account_words`), and the resume's signed text names it.

## Order

After the sign-in fix and the rest of browser batch 2. The code follows this contract once its security review is done:
cloud (Part 1), then planque (Part 2).
