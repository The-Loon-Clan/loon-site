# Three storefronts, two currencies, one money rail

A design for taking USD/crypto directly, alongside the points store.

**Implementation lands in `loon-plugins`, not here.** The flair store is
`loon-plugins/pointstore`, the payment rail is `loon-plugins/donations`, and the
contract between them belongs in `loon-plugins/pluginapi`. That tree runs a live
site and is shared, so this is written to be handed over rather than applied —
the host needs no change at all, which is itself the argument that the design is
in the right place.

## What exists

```
USD / crypto ──BTCPay, HMAC-verified webhook──> points ──> ranks · invites · flair
```

The donations plugin already owns a full payment rail: invoice creation,
click-to-claim, an HMAC-verified settlement webhook, a wallet/credentials admin
console, and an admin-tunable `($, points)` curve. It is dev-only behind
`LOON_DONATIONS` in this host because it takes real money.

Both shops spend points. `/store` (the `store` plugin) sells ranks and invites;
`/p/store` (the `pointstore` plugin) sells flair at `Cost int`, debited with
`core.Points.Deduct`.

## What changes

Three surfaces, and the first two are the SAME store wearing different clothes:

| surface | you choose | settlement grants |
| --- | --- | --- |
| **Donate** | an amount | points, via the curve |
| **USD store** | an item | that item |
| **Points store** | an item | that item, debited from points |

Donate and the USD store are one mechanism. Both raise an invoice, both wait for
the same HMAC-verified webhook, both grant on settlement. The only difference is
what the settlement handler does with the money once it has arrived — and that
is a closure, not a second integration.

Saying it the other way round: a donation is a purchase whose item is points.

That is why the contract below has ONE rail and a registry of consumers rather
than a payment method per shop. If the two were built separately they would
drift — one would get the underpayment check and the other would not, and
nobody would notice until it mattered.

Ranks and invites stay on points, deliberately — see "what stays" below.

## The contract

This is the third cross-plugin capability, and it follows the two that exist
(`RankGranter`, published by ranks and consumed by store; `InviteGranter`,
published by the host). `RankGranter`'s own doc already anticipates this case:

> It is GRANT-ONLY: the caller debits whatever currency the grant costs (points
> via core.Points for a store purchase, **external money via the donation
> flow**) BEFORE calling.

So the shape is settled. A new `pluginapi/checkout.go`:

```go
// CheckoutName is the Core extension-registry key under which the DONATIONS
// plugin publishes its Checkout.
const CheckoutName = "checkout"

// Checkout creates a payment and reports settlement. Published by whichever
// plugin owns the payment rail (donations, today); consumed by any plugin that
// sells something for money.
//
// Deliberately NOT "BTCPay": a consumer must not know how the money arrives, or
// swapping the processor becomes a change in every shop.
type Checkout interface {
	// NewInvoice returns a URL to send the buyer to. It does NOT grant
	// anything — see OnSettled.
	//
	// ref is the caller's own idempotency key, unique per purchase attempt,
	// echoed back on settlement. amountMinor is in the currency's minor unit
	// (cents), because floats have no place in money.
	NewInvoice(ctx context.Context, in InvoiceRequest) (payURL string, err error)

	// OnSettled registers a handler the rail calls when money has ACTUALLY
	// arrived and been verified. Registered by consumer name, which is what
	// lets the donate page and the USD store share one rail: "donate" awards
	// points off the curve, "flair" grants the item, and the invoice, the
	// webhook, the HMAC check and the underpayment rule are written once.
	OnSettled(consumer string, fn SettledFunc) error
}

type InvoiceRequest struct {
	Consumer    string // "flair" — routes settlement back
	Ref         string // unique per attempt; the idempotency key
	UserID      int64
	AmountMinor int64  // 250 = $2.50
	Currency    string // "USD"
	Description string // shown on the invoice
}

// SettledFunc is called at most once per Ref with money confirmed received.
// Returning an error makes the rail retry, so it MUST be idempotent.
type SettledFunc func(ctx context.Context, ref string, userID int64, amountMinor int64) error
```

## The four things that make it safe

These are the properties to test, and each has a specific failure behind it.

**1. Grant on settlement, never on checkout.** If flair is granted when the
invoice is created, anyone can have every colour on the site for free by
starting checkouts and never paying. The whole point of splitting `NewInvoice`
from `OnSettled` is that no code path can grant without the rail having
confirmed money.

**2. Idempotent grants.** A webhook fires more than once — that is normal, not
exceptional, and BTCPay retries on a non-2xx. The `Ref` is the key: record the
purchase against it, and a second settlement for the same `Ref` is a no-op that
still returns 200. Getting this wrong grants twice and, worse, makes the retry
storm look like a successful integration.

**3. Verify the amount, not just the arrival.** Settlement carries what was
actually paid. An invoice raised at $2.50 that settles for $0.01 must not grant
— crypto underpayment is a normal failure mode, not an attack. Compare against
the price recorded at invoice time, not the price now, or an admin editing the
catalogue mid-flight changes what a pending purchase costs.

**4. Degrade when the rail is absent.** `Metadata.Requires` should declare the
capability, exactly as `store` declares its dependency on ranks. With donations
disabled — which is this host's default, since it is behind `LOON_DONATIONS` —
the flair store must render as unavailable rather than as free.

## What stays on points, and why

Ranks and invites — the third surface, unchanged. That is not squeamishness about money; it is the specific
thing that gets private trackers into legal and reputational trouble, and the
points layer is the indirection that makes a contribution a contribution.
Cosmetics are the well-established exception — nobody's standing changes because
somebody bought a colour.

Keeping the split also keeps the flair store honest about what it is: the only
thing on the site you can buy outright is the only thing that confers nothing.

## Migration

Existing flair rows have `Cost int` in points. Two options, and the second is
better:

1. Convert with the donation curve. Wrong direction — the curve maps dollars to
   points for donors, and running it backwards prices flair at whatever the
   curve happens to imply.
2. Price the catalogue in fiat by hand. There are few enough items that an
   admin setting each price is an afternoon, and the prices then mean something
   rather than being derived from an unrelated table.

Keep the points column through the transition rather than dropping it — a
rollback that has to reconstruct prices is not a rollback.

## What the host does

Nothing — with one exception, already done: the menu.

Capabilities travel through the Core extension registry, so neither the route
table nor `loon-site` changes, and the only host-visible effect of the work
above is that the flair store shows a price in dollars.

The naming, though, went in ahead of it. The two shops are labelled for their
currency — **Store** (`/p/store`, the site nav's Other menu) and **Points
store** (`/store`, the account menu under Bonus Points) — which is the pair this
design arrives at, and is a promise the site does not yet keep, since both still
debit points today. Deliberate: renaming a menu twice teaches the reader the
word twice, and the second lesson is the one that sticks. See `navPlacement` in
`admin_views.go`, and the one-word-one-destination rule in
[NAVIGATION.md](NAVIGATION.md).

That is worth stating plainly because it is the test of whether this belongs in
the plugin layer: if the host had to learn about payments, the seam would be in
the wrong place.
