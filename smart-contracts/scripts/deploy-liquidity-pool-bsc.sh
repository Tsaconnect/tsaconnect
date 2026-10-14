#!/usr/bin/env bash
# Deploy TsaLiquidityPool to BSC mainnet with your own key (owner 2026-10-14).
#
#   cd ~/Projects/tsaconnect/tsa-dev-shared-pool/smart-contracts
#   ./scripts/deploy-liquidity-pool-bsc.sh
#
# Asks for the private key without echoing it, shows the address and its BNB,
# and deploys only after you type "deploy". The key lives only in this
# process's environment. Roles come from deploy/chains.js (owner, collateral
# provider and guardian = the owner wallet; operator 0x38cE…; recipient the
# treasury); override with LPOOL_* env vars.
set -euo pipefail
cd "$(dirname "$0")/.."

OWNER="${LPOOL_OWNER:-0x62bA9b24e72E4f1F93f50F2308aCE932066c1b96}"
RPC="${BSC_MAINNET_RPC:-https://bsc-dataseed.bnbchain.org}"

command -v cast >/dev/null || { echo "needs foundry's cast (https://getfoundry.sh)"; exit 1; }
if [ ! -e node_modules ]; then
  ln -s ../../tsa-dev/smart-contracts/node_modules node_modules
  trap 'rm -f node_modules' EXIT
fi

read -rsp "Deployer private key (hidden): " PK; echo
case "$PK" in 0x*) ;; *) PK="0x$PK" ;; esac
ADDR=$(cast wallet address --private-key "$PK") || { echo "not a valid private key"; exit 1; }
BAL=$(cast balance "$ADDR" --rpc-url "$RPC" --ether)
GAS=$(cast from-wei "$(cast gas-price --rpc-url "$RPC")" gwei)
lower() { printf '%s' "$1" | tr '[:upper:]' '[:lower:]'; }

echo "Deployer : $ADDR"
echo "BNB      : $BAL   (gas ${GAS} gwei; the deploy uses ~3.7M gas)"
echo "Owner    : $OWNER$( [ "$(lower "$ADDR")" = "$(lower "$OWNER")" ] && echo '  (= deployer: it keeps ownership)')"
if [ "$(lower "$ADDR")" = "0x38ce9a4c9190707829950b7ae4eed1d254c2e2c9" ]; then
  echo "Refusing: that is the server's OPERATOR key. A hand-sent tx would hold its BSC lane."; exit 1
fi
read -rp 'Type "deploy" to send the transactions to BSC MAINNET: ' GO
[ "$GO" = "deploy" ] || { echo "aborted"; exit 1; }

START=$(cast block-number --rpc-url "$RPC")
DEPLOYER_PRIVATE_KEY="$PK" LPOOL_OWNER="$OWNER" LPOOL_OWNER_CONFIRM="$OWNER" BSC_MAINNET_RPC="$RPC" \
  npx hardhat run deploy/liquidity-pool.js --network bscMainnet
unset PK

echo
echo "Deploy started after block $START. Send me the 'liquidity-pool: ready pool=…' line above,"
echo "plus deployments/bscMainnet.json and .openzeppelin/bsc.json (both written here)."
