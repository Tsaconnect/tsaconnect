#!/usr/bin/env bash
# Deploy TsaLiquidityPool to BSC mainnet with your own key (owner 2026-10-14).
#
#   cd ~/Projects/tsaconnect/tsa-dev/smart-contracts   (any checkout of main)
#   ./scripts/deploy-liquidity-pool-bsc.sh
#
# Takes the key from a Foundry keystore (password prompt) or a hidden paste,
# shows the address and its BNB,
# and deploys only after you type "deploy". The key lives only in this
# process's environment. The DEPLOYER becomes the pool's owner (owner
# 2026-10-15: whoever deploys can upgrade later without waiting on anyone);
# set LPOOL_OWNER to hand it to another wallet instead. The other roles come
# from deploy/chains.js (refill wallet and guardian = 0x62bA…; operator
# 0x38cE…; recipient the treasury); override with LPOOL_* env vars.
set -euo pipefail
cd "$(dirname "$0")/.."

RPC="${BSC_MAINNET_RPC:-https://bsc-dataseed.bnbchain.org}"

command -v cast >/dev/null || { echo "needs foundry's cast (https://getfoundry.sh)"; exit 1; }
if [ ! -e node_modules ]; then
  ln -s ../../tsa-dev/smart-contracts/node_modules node_modules
  trap 'rm -f node_modules' EXIT
fi

# The key comes from a Foundry keystore (~/.foundry/keystores: only its
# password is typed, at cast's own prompt) or, with no keystore, is pasted
# hidden. KEYSTORE=<name> skips the question.
ACCOUNTS=$(ls "${FOUNDRY_KEYSTORES:-$HOME/.foundry/keystores}" 2>/dev/null | tr '\n' ' ' || true)
if [ -z "${KEYSTORE:-}" ] && [ -n "$ACCOUNTS" ]; then
  read -rp "Keystore account [${ACCOUNTS% }] (blank to paste a key instead): " KEYSTORE
fi
if [ -n "${KEYSTORE:-}" ]; then
  PK=$(cast wallet decrypt-keystore ${FOUNDRY_KEYSTORES:+-k "$FOUNDRY_KEYSTORES"} "$KEYSTORE" | grep -oE '0x[0-9a-fA-F]{64}' | tail -1) || true
  [ -n "$PK" ] || { echo "could not decrypt keystore '$KEYSTORE'"; exit 1; }
else
  read -rsp "Deployer private key (hidden): " PK; echo
fi
case "$PK" in 0x*) ;; *) PK="0x$PK" ;; esac
# The key goes to child processes only through the environment, never argv
# (argv is visible to every local process via ps).
ADDR=$(PK="$PK" node -e 'try { console.log(new (require("ethers").Wallet)(process.env.PK).address) } catch { process.exit(1) }') \
  || { echo "not a valid private key"; exit 1; }
BAL=$(cast balance "$ADDR" --rpc-url "$RPC" --ether)
GAS=$(cast from-wei "$(cast gas-price --rpc-url "$RPC")" gwei)
lower() { printf '%s' "$1" | tr '[:upper:]' '[:lower:]'; }
OWNER="${LPOOL_OWNER:-$ADDR}"
PROVIDER="${LPOOL_COLLATERAL_PROVIDER:-0x62bA9b24e72E4f1F93f50F2308aCE932066c1b96}"
GUARDIAN="${LPOOL_GUARDIAN:-0x62bA9b24e72E4f1F93f50F2308aCE932066c1b96}"

echo "Deployer : $ADDR"
echo "BNB      : $BAL   (gas ${GAS} gwei; the deploy uses ~3.7M gas)"
echo "Owner    : $OWNER$( [ "$(lower "$ADDR")" = "$(lower "$OWNER")" ] && echo '  (= deployer: it keeps ownership and can upgrade)')"
echo "Refill   : $PROVIDER   Guardian: $GUARDIAN"
if [ "$(lower "$ADDR")" = "0x38ce9a4c9190707829950b7ae4eed1d254c2e2c9" ]; then
  echo "Refusing: that is the server's OPERATOR key. A hand-sent tx would hold its BSC lane."; exit 1
fi
read -rp 'Type "deploy" to send the transactions to BSC MAINNET: ' GO
[ "$GO" = "deploy" ] || { echo "aborted"; exit 1; }

START=$(cast block-number --rpc-url "$RPC")
DEPLOYER_PRIVATE_KEY="$PK" LPOOL_OWNER="$OWNER" LPOOL_OWNER_CONFIRM="$OWNER" \
  LPOOL_COLLATERAL_PROVIDER="$PROVIDER" LPOOL_GUARDIAN="$GUARDIAN" BSC_MAINNET_RPC="$RPC" \
  npx hardhat run deploy/liquidity-pool.js --network bscMainnet
unset PK

echo
echo "Deploy started after block $START. Send me the 'liquidity-pool: ready pool=…' line above,"
echo "plus deployments/bscMainnet.json and .openzeppelin/bsc.json (both written here)."
