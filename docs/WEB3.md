# rentPC — Web3 integration

*Crypto World's Fair Hackathon · Tempo track.*

## What is on-chain today

**Machine Payments Protocol (MPP) on Tempo** — https://mpp.dev, co-developed
by Tempo and Stripe. rentPC's marketplace charges AI agents (and any API-key
client) for compute **per request, over HTTP 402**, settled in stablecoins on
Tempo. No wallet UI, no custom token: the standard protocol agents already
speak to pay for APIs is the same one they use to rent a PC.

```
agent ──POST /v1/rentals {listing_id, hours}──────────────▶ paygate
      ◀── 402  WWW-Authenticate: Payment … amount=1.00 OUSD ──
      ──(mppx / rentpc-pay signs a Tempo transfer)─────────▶ paygate ──verify on Tempo──▶ RPC
      ◀── 201  Payment-Receipt: …  {rental, ssh_private_key} ── paygate ──X-RentPC-Prepaid (HMAC)──▶ api
```

* `services/paygate` — Go, built on `github.com/tempoxyz/mpp-go`. Issues
  challenges priced from the API's public quote endpoint, verifies the
  credential on-chain (pull or push mode), then forwards the original request
  to the API with a signed prepaid claim.
* `apps/api` — trusts the claim (shared HMAC secret, 2-minute freshness, must
  match listing + hours + amount), reserves the machine, writes the ledger
  (`provider = mpp`, `provider_ref = tx reference`) and skips end-of-rental
  capture. Everything else in the rental lifecycle is unchanged.
* `rentpc-pay` — reference agent client: `POST`, pay the 402 under a
  `--max-spend` budget, retry, print the rental and receipt.

* **In-browser wallet flow** — the machine page has "Pay with a Tempo wallet":
  wagmi + `mppx/client` with a **passkey wallet (nothing to install) or
  MetaMask**, a one-click testnet faucet, then a single pathUSD/OUSD transfer
  signed in the wallet satisfies the 402 and the rental is created. The rental
  page links the on-chain transaction on explore.tempo.xyz.

* **Hosts are paid on-chain, in the renter's transaction.** A host enters a
  Tempo wallet on the host dashboard; the gateway then issues the challenge as
  an MPP **split payment**: `amount = price`, `recipient = treasury`,
  `splits = [{recipient: host wallet, amount: price − 10% fee}]`. The renter
  signs one transfer, the host's share lands in their wallet and the treasury
  keeps only the fee — no custody, no payout batch. The API ledger records the
  gross earning, the fee and a matching `payout` row referencing the tx, so
  "owed to host" is zero for these rentals. Hosts without a wallet accrue a
  balance as before.
* The gateway verifies the caller's API key / session with the API **before**
  issuing a challenge, so a misconfigured agent can't pay for a request the
  API would reject.

Browser users can also rent with the mock provider, so a judge can try the
product without a wallet.

### Why this fits Tempo

Tempo's pitch is stablecoin payments for machines. rentPC is a machine buying
a machine: an agent that needs 2 vCPUs for an hour pays $0.50 in OUSD and gets
SSH credentials in the same HTTP exchange — no account top-ups, no cards, no
human in the loop. Per-hour pricing, extensions and prorated refunds map
naturally onto MPP charges (and, later, MPP sessions for streaming pay-as-you-go).

### Run it

```bash
# on the server (deploy/aws/.env)
PAYGATE_SECRET=$(openssl rand -hex 32)
MPP_SECRET_KEY=$(openssl rand -hex 32)
MPP_RECIPIENT=0x...            # marketplace treasury on Tempo (testnet: Moderato)
RENT_UPSTREAM=paygate:8090
docker compose --profile mpp up -d --build

# as an agent (testnet funds: npx mppx account create --network testnet && npx mppx account fund --network testnet)
export RENTPC_API_KEY=rpk_...  MPP_PRIVATE_KEY=0x...
rentpc-pay --server http://<host> --listing <listing-id> --hours 1 --max-spend 2.00
# or with Tempo's own CLI:
npx mppx fetch -X POST http://<host>/v1/rentals -H "Authorization: Bearer rpk_..." \
  -d '{"listing_id":"...","hours":1}'
```

## Designed-in seams (not yet live)

| Seam | Where | Next provider |
|---|---|---|
| Payments | `payments.Provider` (`Authorize/Capture/Release`) + prepaid claims | **x402 on Solana** (same gateway, second method), MPP sessions for streaming |
| Identity | session cookie / API key (`auth.Service`) | wallet-signature login (Phantom Connect / Privy), agent identity via `mppx` |
| Escrow & refunds | ledger rows per rental; prepaid flag; split payouts | MPP **sessions** for metered per-minute billing with automatic refund of unused time |
| Usage receipts | `usage_samples` + `rental_events` per rental | host + platform co-signed receipt, hash anchored on Tempo/Solana |
| Privacy | — | confidential amounts via Token-2022 Confidential Balances / Arcium |
| Reputation | `reviews`, completed-rental counts | portable, on-chain host reputation keyed to a DID |

The core rental infrastructure (relay, agent, VM lifecycle) imports none of
these; swapping or adding a chain touches `services/paygate` and the
`payments` package only.
