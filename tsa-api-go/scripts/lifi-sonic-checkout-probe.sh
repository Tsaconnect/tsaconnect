#!/usr/bin/env bash
#
# LiFi probe for marketplace "pay with another token" (read-only, no funds)
# Usage: ./scripts/lifi-sonic-checkout-probe.sh [EVM_ADDRESS] [TO_AMOUNT]
#
# A checkout funded with another token is ONE exact-output LiFi swap that must
# land the order token — Sonic USDC or USDT — in the buyer's own wallet on
# Sonic (chain 146). The server refuses a route whose action.toToken is not the
# configured address (checkSignable) or whose calldata hands the funds to a
# destination call (allowDestinationCall=false + checkLiFiDeliveryTo), so if
# LiFi ever starts answering with a bridged variant (USDC.e, USDT0) for these
# pairs, every funding from that source silently becomes "unavailable".
#
# This asks /quote/toAmount for each source below and prints, per pair: the
# toToken LiFi resolved, whether it is the configured address, the tool and
# its ETA, and the cost split. Re-run it whenever LiFi changes Sonic tools, or
# when a source starts failing in production.
#
# Checked 2026-09-29: Base USDC, BSC USDT, Arbitrum/Polygon USDC, ETH USDT and
# Sonic S all resolved to Sonic USDC 0x29219dd4… (polymerStandard ~18 min,
# relaydepository ~2 s, kyberswap on Sonic itself).
#
# EVM_ADDRESS is only the from/to address LiFi prices for; it needs no balance
# for a quote. It must be EIP-55 checksummed.
#
# Examples:
#   ./scripts/lifi-sonic-checkout-probe.sh
#   LIFI_API_KEY=... ./scripts/lifi-sonic-checkout-probe.sh 0xabc... 25000000

set -euo pipefail

BASE="${LIFI_BASE_URL:-https://li.quest/v1}"
ADDR="${1:-0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045}"
TO_AMOUNT="${2:-10000000}" # 10 units of a 6 dp stablecoin

# The configured Sonic order tokens (internal/config/config.go, sonic mainnet preset).
SONIC_USDC="0x29219dd400f2Bf60E5a23d13Be72B486D4038894"
SONIC_USDT="0x6047828dc181963ba44974801ff68e538da5eaf9"

# source chain id | source token | label
SOURCES=(
  "8453|0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913|Base USDC"
  "56|0x55d398326f99059fF775485246999027B3197955|BSC USDT"
  "42161|0xaf88d065e77c8cC2239327C5EDb3A432268e5831|Arbitrum USDC"
  "137|0x3c499c542cEF5E3811e1192ce70d8cC03d5c3359|Polygon USDC"
  "1|0xdAC17F958D2ee523a2206206994597C13D831ec7|Ethereum USDT"
  "146|0x0000000000000000000000000000000000000000|Sonic S"
)

GREEN='\033[0;32m'; RED='\033[0;31m'; CYAN='\033[0;36m'; NC='\033[0m'

# GET with the API key when one is set (see lifi-btc-probe.sh for why this is a
# function and not an argument array: bash 3.2 on macOS).
lifi_get() {
  if [[ -n "${LIFI_API_KEY:-}" ]]; then
    curl -sS -H "x-lifi-api-key: ${LIFI_API_KEY}" "$1"
  else
    curl -sS "$1"
  fi
}

say() { printf "${CYAN}%s${NC}\n" "$1"; }
ok()  { printf "${GREEN}  PASS${NC} %s\n" "$1"; }
bad() { printf "${RED}  FAIL${NC} %s\n" "$1"; FAILED=1; }

command -v jq >/dev/null || { echo "jq is required"; exit 1; }
FAILED=0

lower() { printf '%s' "$1" | tr '[:upper:]' '[:lower:]'; }

probe() {
  local dest_label="$1" dest_token="$2"
  say "Destination: Sonic ${dest_label} (${dest_token})"
  for entry in "${SOURCES[@]}"; do
    IFS='|' read -r chain token label <<<"$entry"
    if [[ "$chain" == "146" && "$(lower "$token")" == "$(lower "$dest_token")" ]]; then
      continue
    fi
    url="${BASE}/quote/toAmount?fromChain=${chain}&toChain=146&fromToken=${token}&toToken=${dest_token}"
    url+="&fromAddress=${ADDR}&toAddress=${ADDR}&toAmount=${TO_AMOUNT}&allowDestinationCall=false&order=FASTEST&slippage=0.005&integrator=tsa-connect"
    body=$(lifi_get "$url" || true)
    if ! echo "$body" | jq -e '.action.toToken.address' >/dev/null 2>&1; then
      bad "${label}: no route ($(echo "$body" | jq -r '.message // "unreadable answer"' 2>/dev/null | head -c 120))"
      continue
    fi
    got=$(echo "$body" | jq -r '.action.toToken.address')
    to_chain=$(echo "$body" | jq -r '.action.toChainId')
    tool=$(echo "$body" | jq -r '.toolDetails.name // .tool')
    eta=$(echo "$body" | jq -r '.estimate.executionDuration // 0')
    from_usd=$(echo "$body" | jq -r '.estimate.fromAmountUSD // "?"')
    to_usd=$(echo "$body" | jq -r '.estimate.toAmountUSD // "?"')
    gas_usd=$(echo "$body" | jq -r '[.estimate.gasCosts[]?.amountUSD | tonumber] | add // 0')
    steps=$(echo "$body" | jq -r '[.includedSteps[]?.tool] | join(" > ")')
    # Pass-through: with no `fee` param we take nothing. LiFi's own fixed fee is
    # part of its cost; our integrator share must read 0.
    integrator=$(echo "$body" | jq -r '[.estimate.feeCosts[]?.feeSplit.integratorFee // empty | tonumber] | add // 0')
    line="${label}: toToken=${got} chain=${to_chain} tool=${tool} eta=${eta}s in=\$${from_usd} out=\$${to_usd} gas=\$${gas_usd} integratorFee=${integrator} steps=[${steps}]"
    if [[ "$integrator" != "0" ]]; then
      bad "${line} — an integrator fee is being charged with no fee param: check the LiFi portal's default fee"
      continue
    fi
    if [[ "$(lower "$got")" == "$(lower "$dest_token")" && "$to_chain" == "146" ]]; then
      ok "$line"
    else
      bad "$line — NOT the configured Sonic ${dest_label}: checkSignable will refuse this route"
    fi
  done
}

probe "USDC" "$SONIC_USDC"
probe "USDT" "$SONIC_USDT"

if [[ "$FAILED" == "1" ]]; then
  echo
  echo "Some sources did not resolve to the configured Sonic token. Those sources cannot fund a checkout until LiFi's route or our config changes."
  exit 1
fi
echo
echo "Every source resolved to the configured Sonic tokens."
