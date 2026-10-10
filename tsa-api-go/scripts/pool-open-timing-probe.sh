#!/usr/bin/env bash
#
# TSA Liquidity pool-open timing probe (READ-ONLY: nothing is signed or sent)
# Usage: ./scripts/pool-open-timing-probe.sh RPC_URL TREASURY ESCROW [SLICES] [ROUNDS]
#
# A pool order must open and prove every slice inside PoolOpenCap (8 s,
# services/pool_open.go). The BSC testnet rehearsal missed it from a laptop
# (0.28 s RTT: 9.7 s), so PoolOpenCap and poolProveWindow are to be sized from
# timings measured where the API runs (pre-merge review I-1), before plan C2
# turns pool orders on for mainnet. Run this on the prod host against each RPC
# in the pool chain's list (one URL per run).
#
# It times, ROUNDS times each, every read in the open sequence:
#
#   once per burst   eth_getBalance (treasury gas), eth_gasPrice
#   prepare (parallel across slices)
#                    eth_getTransactionCount pending, eth_gasPrice,
#                    eth_estimateGas
#   per slice, under the lane lock
#                    eth_getTransactionCount pending, then the broadcast
#                    (eth_sendRawTransaction, NOT sent here: one RTT is assumed)
#   per prove poll (every 300 ms until 2 blocks deep, per slice)
#                    getTrade + trades (eth_call), eth_getTransactionReceipt,
#                    eth_blockNumber
#
# then prints each call's median and an estimate of one open:
#   2·burst + 3·prepare + SLICES·(nonce + send) + 2 blocks + one prove poll.
#
# eth_estimateGas targets the escrow's paymentWindow() view (a real openTrade
# needs a live voucher); the round trip, not the gas, is what is measured.
#
# Examples:
#   ./scripts/pool-open-timing-probe.sh https://bsc-dataseed.bnbchain.org 0xTreasury 0xEscrow
#   ./scripts/pool-open-timing-probe.sh "$RPC" 0xTreasury 0xEscrow 3 9

set -euo pipefail

RPC="${1:?usage: RPC_URL TREASURY ESCROW [SLICES] [ROUNDS]}"
TREASURY="${2:?treasury address}"
ESCROW="${3:?escrow address}"
SLICES="${4:-3}"
ROUNDS="${5:-5}"

GREEN='\033[0;32m'; RED='\033[0;31m'; CYAN='\033[0;36m'; NC='\033[0m'
say() { printf "${CYAN}%s${NC}\n" "$1"; }
ok()  { printf "${GREEN}  OK${NC}   %s\n" "$1"; }
bad() { printf "${RED}  FAIL${NC} %s\n" "$1"; }

command -v jq >/dev/null || { echo "jq is required"; exit 1; }
command -v curl >/dev/null || { echo "curl is required"; exit 1; }

# Every method this script may call. Anything that sends is absent on purpose.
READ_ONLY='eth_chainId eth_blockNumber eth_getBalance eth_gasPrice eth_getTransactionCount eth_estimateGas eth_call eth_getTransactionReceipt'

ZERO_ID="0x$(printf '0%.0s' $(seq 1 64))"
GET_TRADE="0xa3b13799${ZERO_ID#0x}"      # getTrade(bytes32)
TRADES="0x00162420${ZERO_ID#0x}"         # trades(bytes32): the fee snapshot
PAYMENT_WINDOW="0xcab55d56"              # paymentWindow()

# rpc METHOD PARAMS_JSON → prints "<seconds> <result-or-error>"
rpc() {
  case " $READ_ONLY " in *" $1 "*) ;; *) echo "refusing non-read method $1" >&2; exit 2 ;; esac
  local body out
  body=$(jq -cn --arg m "$1" --argjson p "$2" '{jsonrpc:"2.0",id:1,method:$m,params:$p}')
  out=$(curl -sS -o /tmp/pool-open-probe.$$ -w '%{time_total}' -H 'Content-Type: application/json' --data "$body" "$RPC") || {
    echo "0 transport-error"; return; }
  printf '%s %s\n' "$out" "$(jq -c '.result // .error' /tmp/pool-open-probe.$$ 2>/dev/null || echo unparsable)"
}
trap 'rm -f /tmp/pool-open-probe.$$' EXIT

median() { sort -n | awk '{a[NR]=$1} END {if (NR==0) {print 0} else if (NR%2) {print a[(NR+1)/2]} else {print (a[NR/2]+a[NR/2+1])/2}}'; }

declare_call() { # NAME METHOD PARAMS
  local name="$1" method="$2" params="$3" times="" i line t res
  for ((i = 0; i < ROUNDS; i++)); do
    line=$(rpc "$method" "$params")
    t=${line%% *}; res=${line#* }
    times+="$t"$'\n'
  done
  local m; m=$(printf '%s' "$times" | median)
  case "$res" in
    *'"code"'*|transport-error|unparsable) bad "$(printf '%-28s median %6.3f s  last: %s' "$name" "$m" "$res")" ;;
    *) ok "$(printf '%-28s median %6.3f s' "$name" "$m")" ;;
  esac
  eval "T_$name=$m"
}

say "0. Chain"
line=$(rpc eth_chainId '[]'); echo "  chain id ${line#* } (rtt ${line%% *} s)"

say "1. Once per burst"
declare_call balance        eth_getBalance          "[\"$TREASURY\",\"latest\"]"
declare_call gasPrice       eth_gasPrice            '[]'

say "2. Prepare (all slices in parallel)"
declare_call nonce          eth_getTransactionCount "[\"$TREASURY\",\"pending\"]"
declare_call estimate       eth_estimateGas         "[{\"from\":\"$TREASURY\",\"to\":\"$ESCROW\",\"data\":\"$PAYMENT_WINDOW\"}]"

say "3. Prove poll (per slice)"
declare_call getTrade       eth_call                "[{\"to\":\"$ESCROW\",\"data\":\"$GET_TRADE\"},\"latest\"]"
declare_call feeSnapshot    eth_call                "[{\"to\":\"$ESCROW\",\"data\":\"$TRADES\"},\"latest\"]"
declare_call receipt        eth_getTransactionReceipt "[\"$ZERO_ID\"]"
declare_call blockNumber    eth_blockNumber         '[]'

say "4. Block time (two samples 10 s apart)"
b1=$(rpc eth_blockNumber '[]'); sleep 10; b2=$(rpc eth_blockNumber '[]')
n1=$((16#$(echo "${b1#* }" | tr -d '"' | sed 's/^0x//'))); n2=$((16#$(echo "${b2#* }" | tr -d '"' | sed 's/^0x//')))
block=$(awk -v d=$((n2 - n1)) 'BEGIN { if (d > 0) print 10 / d; else print 3 }')
echo "  ~${block} s per block"

say "5. One open, estimated (${SLICES} slices; the cap is 8 s, proving gets 6 s)"
awk -v b="$T_balance" -v g="$T_gasPrice" -v n="$T_nonce" -v e="$T_estimate" -v s="$SLICES" \
    -v tr="$T_getTrade" -v f="$T_feeSnapshot" -v r="$T_receipt" -v h="$T_blockNumber" -v blk="$block" 'BEGIN {
  burst   = b + g
  prepare = n + g + e                  # slowest single slice; slices run in parallel
  sends   = s * (n + n)                # nonce read + broadcast (≈ one RTT) per slice
  poll    = s * (tr + f + r + h)       # one prove pass over every slice
  prove   = 2 * blk + poll
  total   = burst + prepare + sends + prove
  printf "  burst %.2f + prepare %.2f + sends %.2f + prove %.2f (2 blocks %.2f + a poll %.2f) = %.2f s\n",
         burst, prepare, sends, prove, 2 * blk, poll, total
  if (total < 8) printf "  inside PoolOpenCap with %.2f s to spare\n", 8 - total
  else printf "  OVER PoolOpenCap by %.2f s: resize PoolOpenCap/poolProveWindow before plan C2\n", total - 8
}'
