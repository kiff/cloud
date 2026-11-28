# K4: universal classes and sign-in identity (contract, for review before code)

**Rule (owner):** nothing per vendor but guides, words and listings. A tool's class comes from what the vendor declares about
it, by one rule for every app. A reviewed per-app table may only raise a class (an override), never be needed to keep a tool
safe. A resumed sign-in needs the vendor to say whose account it is, by one rule for every app.

## Part 1: classes from what the vendor declares (KIFF's gateway)

A tool's class is the worst any of these gives. Every signal may **raise** a class alone. A class can be **lowered** below
`irreversible` only when two independent signals agree.

**What vendors send today**, read from the test account's live grants: no vendor sends `destructiveHint`, not even on deletes,
and no MCP tool declares the scope it needs. planque keeps `read_only` (the vendor's `readOnlyHint`, which planque's own check
must agree with) and `destructive_hint` (only when `true`). So the names carry most of the weight, and unknown-is-closed does
the rest.

1. **The vendor's MCP annotations**, as planque relays them:
   - `destructiveHint: true` raises to at least `irreversible` (`delete` for a delete verb), as today;
   - `readOnlyHint: true` (planque's `read_only`) is one signal toward `read`, never enough alone (step 4).
2. **The verbs in the tool's name**, as today (`class.go`):
   - delete verbs give `delete`;
   - send, share, export, forward, finalize, void, cancel, approve, sign, mark and the rest give `irreversible`;
   - a read verb (`get_`, `list_`, `search_`, `read_`, `show_`) is one signal toward `read`.
3. **The nouns in the tool's name**, generic and never per vendor, each raising whatever the verb, on any tool that isn't
   a read by step 4 (a two-signal read stays a read whatever its name says: `list_cards` lists cards and moves no money,
   as today):
   - **money nouns** give `money`: `transfer`, `payment`, `payout`, `refund`, `charge`, `card` (a payment card), `debit`
     (direct debit, SEPA), `sepa`, `beneficiar` (beneficiary), `payment_link`, `invoice_pay`;
   - **people and access nouns** give `irreversible`: `membership`, `member`, `team`, `role`, `permission`, `invite`, `user`,
     `bank_account`, `api_key`, `webhook`, `oauth`.
4. **Lowering needs two:** a tool is `read` only when `readOnlyHint` (planque's `read_only`) **and** a read verb agree, and
   the vendor didn't mark it destructive (today's rule, kept). It's `write` only when its name has a verb this list knows (create, update,
   add, set, move, upload, attach and the like) and nothing raised it.
5. **The connection's money mark** (a profile's `money: true`): `money`, as today.
6. **Unknown is closed:** a tool nothing above classes (no annotation, a name with no known verb) is `irreversible`, closed on
   the Card until the owner opens it. Today the same tool is a `write`, which is open.

**No tool classes lower than today's tables under this rule.** Each of these is a test:

| Tool | Today (`qonto.json`) | Under this rule |
|---|---|---|
| `create_transfer_request` | money | money (`transfer`) |
| `create_multi_transfer_request` | money | money (`transfer`) |
| `create_card` | money | money (`card`) |
| `update_card` | money | money (`card`) |
| `change_card_status` | money | money (`card`) |
| `create_membership` | irreversible | irreversible (`membership`) |
| `create_team` | write | irreversible (`team`): stricter than today |

The test suite also checks every table tool for every app: the rules alone never class it lower than its table.

**Per-app tables (`classes/<app>.json`) become overrides.** An override may raise a class freely. It may lower one only with
its reason written beside it and reviewed in the PR that adds it, as `qonto.json`'s draft condition does today, and never
lowers a `money` tool. A tool missing from every table is still classed by the rule, never left open.

**What changes for live accounts:**
- Tools that were `read` on one signal (planque's read_only plus a read verb) stay `read`, since that's two signals.
- Tools that were `write` only because nothing matched become `irreversible` and closed. **Each such tool on a live Card is
  listed before the switch**, and the owner's Cards keep opening what they opened by name: a Card that names a tool opens it
  whatever its class.
- Behind `KIFF_K4_UNIVERSAL` per account, as K4 itself was, read back from the running API before an account is switched.

### Worked examples

| App | Tool | Signals | Class |
|---|---|---|---|
| Qonto | `list_client_invoices` | readOnlyHint, verb `list_` | `read` (two agree) |
| Qonto | `get_organization` | read_only (planque), verb `get_` | `read` |
| Qonto | `create_client` | verb `create_` | `write` |
| Qonto | `create_client_invoice` | verb `create_` + `invoice` | `irreversible` (the draft pin lowers a call with `status: draft` to `write`, as today) |
| Qonto | `send_client_invoice` | verb `send_` | `irreversible` |
| Qonto | `create_transfer_request` | noun `transfer` | `money` |
| Qonto | a SEPA direct-debit tool | nouns `sepa`, `debit` | `money` |
| Qonto | a membership or team write | nouns `membership`, `team` | `irreversible` |
| Qonto | a bank-account write | noun `bank_account` | `irreversible` |
| Qonto | `show_view` | readOnlyHint, verb `show_` | `read` (two agree) |
| Stripe | `stripe_api_write` pinned to `PostInvoicesInvoiceVoid` | operation verb `void` | `irreversible` |
| Stripe | `list_customers` | readOnlyHint, verb `list_` | `read` |
| Composio | `GMAIL_SEND_EMAIL` | app prefix dropped, verb `send_` | `irreversible` |
| Composio | `GMAIL_LIST_THREADS` | readOnlyHint, verb `list_` after the prefix | `read` |
| Composio | `NOTION_DO_THING` (made up) | no annotation, no known verb or noun | `irreversible`, closed |

**Qonto's 32 granted scopes:** a grant's scopes say what the sign-in allows as a whole, not what each tool needs, so they
classify no tool. They decide only which tools the grant lists. The tools those scopes add (direct debits, payment links, bank
accounts, team and membership writes) are classed by their nouns above, and each is checked on the fresh full-scope list.

**The one edge, settled by the security review:** `readOnlyHint` alone never makes a tool `read`. MCP calls these hints: the
vendor's own claim, unchecked, and relayed from servers we don't control. A hint may raise a class alone and never lower one.

**Tests the code PR carries:**
- a tool named `list_` whose vendor sends no `readOnlyHint` is no `read` (one signal), and one named `list_cards` with it is
  a `read`, not `money`;
- `readOnlyHint` on `create_payment` stays `money`;
- a tool nothing classes is `irreversible`;
- an override lowers only with its reason, and never a `money` tool.

## Part 2: a generic identity read at every callback (planque)

At each OAuth callback, planque reads whose account the sign-in is, by one rule:
1. the provider's OpenID userinfo (`sub`, `email`, `name`), or token introspection, where offered (neither Qonto nor Stripe
   publishes one today);
2. else **the MCP server's identity tool, found by one rule:** a tool the vendor marks read-only whose name says it returns
   the signed-in account (`authenticated`, `whoami`, `me`, `current_user`, `account`, `organization`, `organisation`),
   called once at the callback with no arguments. Qonto's `get_authenticated_membership` and `get_organization` and Stripe's
   `list_available_accounts_or_orgs` fit. Several fit: the first in that word order. Its answer gives `id` and a label (a
   name, legal name or email) by the same field names for every app;
3. else **empty**.

- What's kept is `account: {id, label}`: an id the vendor gives and a label for the owner. No token, no other field.
- **Empty means no resume** for that app: a sign-in that can't say whose account it is can't be resumed on another device
  or after a lapse, and fails closed to a fresh sign-in.
- The owner sees the label in the words planque already sends (`account_words`), and the resume's signed text names it. No
  new approval words: the resume reuses the signed-in words the phone already shows.
- **The identity read is recorded** in the connection's record, like any call planque makes for it, so "every call is
  recorded" stays true. It's outside any Card: it reads only whose account it is.
- **The privacy page's account sentence changes first:** a resume finishes only with the same account, read once at sign-in.
  The new text ships before the identity read, or `KIFF_K4_UNIVERSAL`, is on for any customer.

## Order

After the sign-in fix and the rest of browser batch 2. The code follows this contract once its security review is done:
cloud (Part 1), then planque (Part 2).
