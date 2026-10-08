#!/usr/bin/env bash
# liquidity-local-boot.sh — boot the API locally for a TSA Liquidity check,
# refusing anything that could touch production (Plan A review I7).
#
#   scripts/liquidity-local-boot.sh <testnet-env-file> [seconds]
#
# <testnet-env-file> is written by the operator for this purpose — NEVER
# tsa-api-go/.env, never a Doppler export. The server runs from an empty
# directory (so godotenv finds no .env) with ONLY that file's allow-listed
# variables.
set -euo pipefail
ENV_FILE=${1:?usage: liquidity-local-boot.sh <testnet-env-file> [seconds]}
SECS=${2:-45}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
fail() { echo "liquidity-local-boot: refusing — $*" >&2; exit 2; }
case "$(basename "$ENV_FILE")" in .env|.env.*) fail "pass a testnet-only file, not $ENV_FILE";; esac
# Allow-list (review M5): only the keys the rehearsal needs reach the server.
# Anything else — a provider credential, another signer key, a future secret —
# is refused by name, without being listed here.
ALLOWED=(ENV SONIC_NETWORK BSC_NETWORK DATABASE_URL JWT_SECRET PORT
  SONIC_TESTNET_RPC_URL BSC_TESTNET_RPC_URL BSC_TESTNET_USDT_ADDRESS
  PM_RELEASE_PRIVATE_KEY PM_ADMIN_EOA_ADDRESS IT_PM_ID RAMP_CONVERSION_BUY
  RAMP_TREASURY_PRIVATE_KEY RAMP_TREASURY_CHAINS RAMP_TREASURY_NATIVE_FLOOR_WEI_BSC
  RAMP_TREASURY_MAX_INFLIGHT_USD_PER_ORDER RAMP_TREASURY_MAX_INFLIGHT_USD_PER_CHAIN)
allowed() { local k; for k in "${ALLOWED[@]}"; do [ "$k" = "$1" ] && return 0; done; return 1; }
# The file is parsed, never sourced: every line is blank, a comment, or
# KEY='value' / KEY=bare-value. `export KEY=`, command substitution, `;` and `&`
# are refused rather than run.
SERVER_ENV=()
n=0
while IFS= read -r line || [ -n "$line" ]; do
  n=$((n + 1))
  [[ "$line" =~ ^[[:space:]]*(#.*)?$ ]] && continue
  [[ "$line" =~ ^([A-Z][A-Z0-9_]*)=(\'[^\']*\'|[A-Za-z0-9_.:/@%+,-]*)$ ]] \
    || fail "line $n is not KEY='value' (no export, no shell): ${line%%=*}"
  key=${BASH_REMATCH[1]}; val=${BASH_REMATCH[2]}
  allowed "$key" || fail "$key is not on the allow-list (no live provider credentials, no other signer keys)"
  val=${val#\'}; val=${val%\'}
  SERVER_ENV+=("$key=$val")
done < "$ENV_FILE"
get() { local kv v=""; for kv in ${SERVER_ENV[@]+"${SERVER_ENV[@]}"}; do [ "${kv%%=*}" = "$1" ] && v=${kv#*=}; done; printf '%s' "$v"; }

[ "$(get ENV)" = development ] || fail "ENV must be development"
[ "$(get SONIC_NETWORK)" = testnet ] || fail "SONIC_NETWORK must be testnet"
[ "$(get BSC_NETWORK)" = testnet ] || fail "BSC_NETWORK must be testnet"
case "$(get RAMP_CONVERSION_BUY)" in ""|false|0) ;; *) fail "RAMP_CONVERSION_BUY must be off";; esac
DB=$(get DATABASE_URL)
[[ "$DB" =~ ^postgres(ql)?://[^@/]*@(localhost|127\.0\.0\.1)(:[0-9]+)?/tsa_liq_[a-z0-9_]+(\?.*)?$ ]] \
  || fail "DATABASE_URL must be a local throwaway database named tsa_liq_*"

BIN="$(mktemp -d)/liq-api"; RUN="$(mktemp -d)"; LOG="$RUN/boot.log"
(cd "$ROOT" && go build -o "$BIN" ./cmd/server)
(cd "$RUN" && exec env -i PATH="$PATH" HOME="$HOME" "${SERVER_ENV[@]}" "$BIN") > "$LOG" 2>&1 &
PID=$!
trap 'kill "$PID" 2>/dev/null || true' EXIT
for _ in $(seq "$SECS"); do
  sleep 1
  # A mainnet chain in the P2P registry or the release signer is a hard stop.
  if grep -qE 'P2P (sonic|bsc)\((146|56)\)|release signer ready.*chains=\[([0-9]+ )*(146|56)( [0-9]+)*\]' "$LOG"; then
    kill "$PID"; fail "a MAINNET chain id appeared in the boot log — stopped; see $LOG"
  fi
  kill -0 "$PID" 2>/dev/null || break
done
grep -E '\[config\] P2P|\[liquidity\]|\[pm\] release signer|ALERT' "$LOG" || true
echo "full log: $LOG"
