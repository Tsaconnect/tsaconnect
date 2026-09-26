#!/usr/bin/env bash
#
# Captures LiFi calldata for Base USDC -> Solana routes, so the receiver
# decoder is written against what LiFi actually sends.
#   scripts/lifi-nonevm-calldata-probe.sh <solana-receiver> <outdir>
#
# A Solana destination is the one case where BridgeData.receiver does not name
# the recipient: LiFi puts the placeholder 0x11f1...f1 there and the real
# 32-byte Solana address lives in the tool's own facet struct, at a position
# that differs per tool. checkLiFiDeliveryTo can therefore only vouch for a
# tool whose layout was captured here; anything else is refused. Re-run this
# whenever LiFi routes a Solana destination through a new tool, drop the
# capture into internal/services/testdata/lifi_calldata/ as
# <tool>_base_solana_<sol|usdc>.json, and add the tool's selector + receiver
# position to nonEVMReceiverLayouts in internal/services/lifi_calldata.go.
set -euo pipefail
RECV="$1"; OUT="$2"; mkdir -p "$OUT"
FROM=0x4B0897b0513fdC7C541B6d9D7E929C4e5364D2dB
for TO_TOKEN in 11111111111111111111111111111111 EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v; do
  curl -s "https://li.quest/v1/quote?fromChain=8453&toChain=1151111081099710&fromToken=0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913&toToken=$TO_TOKEN&fromAmount=15000000&fromAddress=$FROM&toAddress=$RECV&allowDestinationCall=false" \
    | python3 -c "import json,sys; d=json.load(sys.stdin); print(json.dumps({'capturedAt':'$(date +%F)','tool':d.get('tool'),'fromChainId':8453,'toChainId':1151111081099710,'toToken':'$TO_TOKEN','receiver':'$RECV','to':d['transactionRequest']['to'],'data':d['transactionRequest']['data']}, indent=1))" \
    > "$OUT/probe_$TO_TOKEN.json"
  sleep 1
done
