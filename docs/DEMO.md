# rentPC — demo runbook

Live site: https://44.222.123.73.sslip.io · Tempo testnet (Moderato) · explorer: https://explore.testnet.tempo.xyz

Three acts: a host lists a Mac from the latest release, a human rents it paying
with a Privy wallet, then an AI agent rents it paying by itself over MPP.

Accounts used: **kavish** (owns the Mac) and **agent** (renter; holds the API
key). A user cannot rent their own machine, so always rent as `agent`.

Listing id of the Mac: `294b79de-ad53-434d-abf5-e7835bf4af06`
(the id in the machine page URL; it never changes). A **rental id** is
different: it is created per booking and appears in the rental page URL and
in the agent's JSON output.

---

## Act 1 — Host: install from the latest release and list the Mac

On the Mac (Terminal):

```bash
mkdir -p ~/rentpc && cd ~/rentpc
curl -fsSL -o rentpc-host https://github.com/kavishshahh/rentPC/releases/latest/download/rentpc-host-darwin-arm64
chmod +x rentpc-host
./rentpc-host doctor
```

Browser, as **kavish**: Host → *Add a machine* → macOS → copy the register
token shown on the page, then:

```bash
./rentpc-host register --server https://44.222.123.73.sslip.io --token <token-from-page>
./rentpc-host run
```

(Already registered? Skip `register`; just `./rentpc-host run`.)

The machine turns *online*. On its page set the listing ($0.50/h, 2 vCPU,
4 GB, 20 GB). On the Host dashboard, *Get paid on-chain* holds the host wallet
`0x6A77FE4F767Cbe5FA9d735da8DBdD41bCAecE13E`.

Check from anywhere:

```bash
curl -s "https://44.222.123.73.sslip.io/v1/market/294b79de-ad53-434d-abf5-e7835bf4af06/quote?hours=1"
```

Expect `"available":true`, `"host_payout_cents":45`, `"host_payout_address":"0x6A77…"`.

---

## Act 2 — Human: pay with a Privy wallet, verify on the explorer

Second browser window, logged in as **agent**:

1. Marketplace → *Apple M1 · 2 vCPU · 4 GB RAM*.
2. **Log in with email (Privy)** → email → code. Panel shows `0x… via Privy`.
   (Or **Connect (Passkey)** for a Tempo passkey wallet.)
3. **Get testnet pathUSD (faucet)** → balance appears.
4. **Pay $0.50 on Tempo & rent** → one signature → rental page.
5. Click the badge **paid via MPP on Tempo · 0x…** → explorer. *Balance
   Updates → See all*: renter −0.50, host `0x6A77…` **+0.45**, treasury
   `0x5fE1…` **+0.05**. One transaction pays host and platform.
6. Rental page → status *ready* → browser terminal:

```bash
nproc; free -h
python3 -c "import urllib.request; print(urllib.request.urlopen('https://pypi.org').status)"
curl -sS --max-time 3 http://192.168.1.1 || echo "LAN blocked"
curl -sS --max-time 3 http://10.0.2.2  || echo "host blocked"
```

7. **Terminate** — VM wiped, listing available again.

---

## Act 3 — Agent: pay and rent directly, no browser

Windows PowerShell. Install the agent client from the same release:

```powershell
mkdir $env:USERPROFILE\rentpc-agent; cd $env:USERPROFILE\rentpc-agent
Invoke-WebRequest -Uri https://github.com/kavishshahh/rentPC/releases/latest/download/rentpc-pay-windows-amd64.exe -OutFile rentpc-pay.exe
Invoke-WebRequest -Uri https://github.com/kavishshahh/rentPC/releases/latest/download/rentpc-windows-amd64.exe -OutFile rentpc.exe
```

Credentials (set before recording): the API key from the **agent** user's
Account page, and a Tempo private key funded by the testnet faucet.

```powershell
$env:RENTPC_API_KEY = "rpk_..."
$env:MPP_PRIVATE_KEY = "0x..."
$L = "294b79de-ad53-434d-abf5-e7835bf4af06"
```

Fund a fresh agent key (once):

```powershell
Invoke-RestMethod -Uri https://rpc.moderato.tempo.xyz -Method Post -ContentType application/json -Body '{"jsonrpc":"2.0","id":1,"method":"tempo_fundAddress","params":["0xAGENT_ADDRESS"]}'
```

Budget guard — the agent refuses a machine above its budget, nothing is spent:

```powershell
.\rentpc-pay.exe --server https://44.222.123.73.sslip.io --listing $L --hours 1 --max-spend 0.10
```

Real purchase:

```powershell
.\rentpc-pay.exe --server https://44.222.123.73.sslip.io --listing $L --hours 1 --max-spend 2.00
```

Output, in order:

```
paying 500000 (base units) of 0x20c0…0000 to 0x5fE1…ADb37 on Tempo…   ← 402 accepted under budget, signed
payment receipt: 0x<tx hash> (success)                                 ← verified on-chain by the gateway
{"rental":{"id":"<rental-id>", ...}, "ssh_private_key":"-----BEGIN OPENSSH PRIVATE KEY-----..."}
```

Explorer: `https://explore.testnet.tempo.xyz/tx/<tx hash>` — same split,
0.45 to the host, 0.05 to the platform, signed by the agent's key.

The agent uses the machine over SSH. Save the `ssh_private_key` value from
the JSON to `agent.key` (keep the BEGIN/END lines), then:

```powershell
icacls agent.key /inheritance:r /grant:r "$($env:USERNAME):R"
ssh -i agent.key -o ProxyCommand=".\rentpc.exe tunnel --server https://44.222.123.73.sslip.io <rental-id>" renter@rentpc
```

Inside the VM:

```bash
pip install requests
python3 -c "import requests; print(requests.get('https://api.github.com').status_code)"
exit
```

Finish: as **agent** → My rentals → Terminate. As **kavish** → Host
dashboard: earnings updated; explorer for `0x6A77…` shows the +0.45s.

---

## Same thing with Tempo's own tooling (optional)

Any MPP client can rent a PC; nothing rentPC-specific is required:

```bash
# inspect the 402 challenge without paying (macOS/Linux)
npx mppx --inspect -X POST -H "Authorization: Bearer rpk_..." \
  -d '{"listing_id":"294b79de-ad53-434d-abf5-e7835bf4af06","hours":1}' \
  https://44.222.123.73.sslip.io/v1/rentals
```

---

## Reset / troubleshooting

```bash
# is the Mac free?
curl -s "https://44.222.123.73.sslip.io/v1/market/294b79de-ad53-434d-abf5-e7835bf4af06/quote?hours=1" | grep -o '"available":[a-z]*'
```

* `available:false` → a rental is still live; terminate it from the renter's *My rentals*.
* Mac shows offline → on the Mac: `pkill -f qemu-system-aarch64; ./rentpc-host run`.
* Server logs: `ssh -i deploy/aws/rentpc-key.pem ubuntu@44.222.123.73`, then
  `cd /opt/rentpc/deploy/aws && docker compose logs --tail=20 paygate api`.
  A good payment logs `402 issued … split=true` then
  `payment verified … host_payout_cents=45`.
* Privy login fails → Privy dashboard → app → allowed origins must include
  `https://44.222.123.73.sslip.io`.
