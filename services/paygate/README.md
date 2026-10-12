# paygate — MPP payment gateway (Tempo)

Sits in front of `POST /v1/rentals` for programmatic clients (API-key
callers such as AI agents). Implements the Machine Payments Protocol
(https://mpp.dev, Tempo + Stripe):

1. Client `POST /v1/rentals {listing_id, hours}` with `Authorization: Bearer rpk_…`.
2. Gateway prices the request via the API's public `/v1/market/{id}/quote`
   and answers **HTTP 402** with a `WWW-Authenticate: Payment …` challenge
   (amount in OUSD on Tempo, recipient = marketplace treasury).
3. Client pays (mppx / rentpc-pay) and retries with `Payment-Authorization`.
4. Gateway verifies the credential on Tempo, then forwards the original
   request to the API with a signed `X-RentPC-Prepaid` claim; the API
   reserves the machine and records the ledger entries as prepaid.
5. The response carries `Payment-Receipt`.

Browser sessions pass straight through (mock provider) unless the page sends
`X-RentPC-Pay: mpp` — which the "Pay with Tempo wallet" button does, using
`mppx/client` with the connected wagmi wallet (passkey or MetaMask).

This module needs Go 1.26 (mpp-go requirement) and therefore lives apart from
`apps/api` (Go 1.24). It is built by `deploy/aws/docker-compose.yml`.

## Env

| var | meaning |
|---|---|
| `LISTEN` | default `:8090` |
| `API_URL` | upstream API, e.g. `http://api:8080` |
| `PAYGATE_SECRET` | shared with the API; signs prepaid claims |
| `MPP_SECRET_KEY` | ≥32 bytes, signs MPP challenges (`openssl rand -hex 32`) |
| `MPP_RECIPIENT` | Tempo address that receives payments |
| `MPP_REALM` | realm shown in challenges, e.g. your API host |
| `TEMPO_RPC_URL` | default `https://rpc.moderato.tempo.xyz` (testnet) |
| `MPP_CURRENCY` | token address; the code defaults to OUSD, but `deploy/aws` pins pathUSD (`0x20c0…0000`), the token the faucet hands out |

## Agent CLI

`rentpc-pay` rents with an automatic 402 → pay → retry loop:

```
export RENTPC_API_KEY=rpk_...  MPP_PRIVATE_KEY=0x...   # Tempo testnet key with OUSD
rentpc-pay --server http://<host> --listing <listing-id> --hours 1 --max-spend 2.00
```
