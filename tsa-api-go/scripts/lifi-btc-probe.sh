#!/usr/bin/env bash
#
# LiFi Bitcoin route probe
# Usage: ./scripts/lifi-btc-probe.sh [BTC_ADDRESS] [EVM_ADDRESS] [SATS]
#
# The Bitcoin swap path (app: services/chains/btcLifi.ts) reads three things
# out of a LiFi UTXO quote and refuses to broadcast if any of them looks
# different than expected:
#
#   transactionRequest.to     the deposit address to pay
#   transactionRequest.value  the amount, in SATS, and equal to the quoted
#                             fromAmount (a mismatch aborts the swap)
#   transactionRequest.data   an optional OP_RETURN memo, hex, <= 80 bytes
#
# Those expectations were written against LiFi's documented shape without a
# live call (CI/dev sandboxes cannot reach li.quest). Run this from anywhere
# that can, before enabling Bitcoin swaps in production, and again whenever
# LiFi changes its UTXO providers.
#
# Examples:
#   ./scripts/lifi-btc-probe.sh
#   LIFI_API_KEY=... ./scripts/lifi-btc-probe.sh bc1q... 0xabc... 200000

set -euo pipefail

BASE="${LIFI_BASE_URL:-https://li.quest/v1}"
BTC_ADDR="${1:-bc1qcr8te4kr609gcawutmrza0j4xv80jy8z306fyu}"
EVM_ADDR="${2:-0x742d35Cc6634C0532925a3b844Bc9e7595f2bD18}"
SATS="${3:-200000}"

GREEN='\033[0;32m'; RED='\033[0;31m'; CYAN='\033[0;36m'; NC='\033[0m'

hdr=()
if [[ -n "${LIFI_API_KEY:-}" ]]; then hdr=(-H "x-lifi-api-key: ${LIFI_API_KEY}"); fi

say() { printf "${CYAN}%s${NC}\n" "$1"; }
ok()  { printf "${GREEN}  PASS${NC} %s\n" "$1"; }
bad() { printf "${RED}  FAIL${NC} %s\n" "$1"; }

command -v jq >/dev/null || { echo "jq is required"; exit 1; }

say "1. Bitcoin in /chains?chainTypes=UTXO"
chains=$(curl -sS "${hdr[@]}" "${BASE}/chains?chainTypes=UTXO")
echo "$chains" | jq -r '.chains[] | "  id=\(.id) key=\(.key) coin=\(.coin) chainType=\(.chainType)"'
btc_chain_id=$(echo "$chains" | jq -r '.chains[] | select(.coin == "BTC") | .id' | head -1)
if [[ "$btc_chain_id" == "20000000000001" ]]; then
  ok "chain id matches BTC_LIFI_CHAIN_ID in services/lifi.ts"
else
  bad "chain id is ${btc_chain_id:-<none>}, but services/lifi.ts seeds 20000000000001"
  echo "       (the app re-learns UTXO ids from /chains, so this is a stale-constant warning,"
  echo "        not a breakage — update BTC_LIFI_CHAIN_ID to keep the seed honest)"
fi

say "2. BTC token entry in /tokens?chainTypes=UTXO"
curl -sS "${hdr[@]}" "${BASE}/tokens?chainTypes=UTXO" \
  | jq -r --arg c "$btc_chain_id" '.tokens[$c][] | "  address=\(.address) symbol=\(.symbol) decimals=\(.decimals)"'

say "3. BTC -> USDC(Arbitrum) quote: the deposit instructions"
quote=$(curl -sS "${hdr[@]}" "${BASE}/quote?fromChain=${btc_chain_id}&toChain=42161&fromToken=bitcoin&toToken=USDC&fromAmount=${SATS}&fromAddress=${BTC_ADDR}&toAddress=${EVM_ADDR}&slippage=0.005")
if [[ "$(echo "$quote" | jq -r '.transactionRequest // "null"')" == "null" ]]; then
  bad "no transactionRequest — LiFi said: $(echo "$quote" | jq -r '.message // .' | head -3)"
  exit 1
fi
echo "$quote" | jq '{fromAmount: .estimate.fromAmount, transactionRequest}'

to=$(echo "$quote"    | jq -r '.transactionRequest.to // ""')
value=$(echo "$quote" | jq -r '.transactionRequest.value // ""')
data=$(echo "$quote"  | jq -r '.transactionRequest.data // ""')

[[ -n "$to" ]] && ok "deposit address present: $to" || bad "no transactionRequest.to — btcLifi.ts aborts"

# value must be sats and equal the quoted fromAmount, else the app refuses.
value_dec=$(python3 -c "import sys; v=sys.argv[1]; print(int(v,16) if v.lower().startswith('0x') else int(v))" "$value" 2>/dev/null || echo "")
if [[ "$value_dec" == "$SATS" ]]; then
  ok "value is ${value} = ${SATS} sats (matches the quoted fromAmount)"
else
  bad "value is ${value} (parsed: ${value_dec:-unparseable}), expected ${SATS} sats"
  echo "       btcLifi.ts REFUSES this route ('This route changed the amount to send')."
  echo "       If LiFi legitimately quotes a different deposit amount, that guard needs revisiting."
fi

memo="${data#0x}"
if [[ -z "$memo" ]]; then
  ok "no memo on this route (deposit-address bridge) — supported"
elif [[ "$memo" =~ ^[0-9a-fA-F]+$ ]] && (( ${#memo} % 2 == 0 )) && (( ${#memo} / 2 <= 80 )); then
  ok "memo is $(( ${#memo} / 2 )) bytes of hex: $(python3 -c "print(bytes.fromhex('$memo').decode('utf-8','replace'))")"
else
  bad "memo is not hex we can put in an OP_RETURN: ${data}"
  echo "       btcLifi.ts aborts rather than send a deposit the bridge cannot match."
fi
