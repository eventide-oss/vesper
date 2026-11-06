# ADR-0004: Count the TTL of a preview from its creation

- **Status:** Accepted
- **Date:** 2026-11-06
- **Deciders:** @damnthonyy
- **Issue / PR:** #38

## Context

A preview is ephemeral: `spec.ttl` says how long it lives, and the operator
deletes it when that time has passed. Several questions follow, and the
answers shape the API, the CLI (`vesperctl extend`, #47) and what users can
expect:

- From when does the TTL count?
- How does a user extend a preview, and up to when?
- Whose clock decides, and what happens when the operator is not running at
  the expiry?
- What does "expired" do: delete the preview, or only stop it?
- What are the minimum and maximum TTL?

Constraints: the operator may restart at any time and keeps no state outside
the cluster; the API must stay declarative (`kubectl apply` of the same object
gives the same result); the expiry must be testable without waiting hours.

## Decision

- **The TTL counts from the creation of the preview.** The expiry is
  `metadata.creationTimestamp + spec.ttl`. Both values are in the object, so
  the operator computes the expiry at every reconciliation and keeps no state.
  It writes it to `status.expiresAt` for people and tools to read.
- **Extending a preview means raising `spec.ttl`.** The expiry moves with it.
  The maximum TTL, 168h (7 days), therefore bounds the whole life of a
  preview, extensions included. Lowering the ttl brings the expiry closer, and
  may expire the preview at once.
- **The operator compares its own clock with the creation time set by the API
  server.** A skew of a few seconds between the two clocks does not matter for
  TTLs counted in minutes or hours.
- **An expiry can be late, never early.** The operator asks to be called back
  at the expiry (`RequeueAfter`) instead of polling. If it is stopped at that
  time, it lists every preview when it starts again, and deletes the ones that
  expired. The expiry is rounded up to the second.
- **An expired preview is deleted.** The operator marks it `Expired` (phase,
  and the `Ready` condition with the reason `Expired`), then deletes it; the
  finalizer deletes its namespace (#39).
- **The TTL is between 1 minute and 168 hours, 24 hours by default.** The
  minimum is low so that an expiry can be observed on a real cluster in tests
  and demos; a short TTL is harmless, the preview only goes sooner.

## Alternatives considered

**Count the TTL from the last change of the preview.** Every `kubectl apply`
would then extend it, often without the user meaning to, and the operator
would need to store when the last change happened, which the object does not
record reliably. A preview could also live forever if something kept updating
it.

**Store the expiry in the spec (`spec.expiresAt`).** Explicit and easy to
extend to a precise time, but the user has to compute an absolute time, an
absolute time in a manifest goes stale when the manifest is applied again
later, and the API server cannot bound it with a simple rule: the maximum
lifetime would have to be checked against the creation time by a webhook.

**Stop the preview at expiry instead of deleting it** (scale it to zero and
keep the namespace). A user could bring it back, but the preview would keep
its namespace, its Service and later its address, and nobody would clean them
up: this is the cost Vesper exists to remove. Deleting is simple and matches
the promise of an ephemeral environment; a new preview can be created from
the same manifest.

## Consequences

- No state outside the cluster: a restarted operator computes the same
  expiries, and a status that is lost is written again.
- `vesperctl extend --by 2h` (#47) patches `spec.ttl` and checks the 168h
  limit; the API server enforces the same limit for `kubectl`.
- A preview cannot live more than 7 days. If teams need longer, the maximum is
  one validation rule to change, and this ADR to revisit.
- Tests inject a fake clock into the reconciler, and the e2e test can use a
  one-minute TTL.
- Expiry deletes everything: a preview cannot be recovered after its TTL.
