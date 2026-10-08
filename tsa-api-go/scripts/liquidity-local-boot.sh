#!/usr/bin/env bash
# liquidity-local-boot.sh — boot the API locally for a TSA Liquidity check,
# refusing anything that could touch production (Plan A review I7).
#
#   scripts/liquidity-local-boot.sh <testnet-env-file> [seconds]
#
# <testnet-env-file> is written by the operator for this purpose — NEVER
# tsa-api-go/.env, never a Doppler export. The server runs from an empty
# directory (so godotenv finds no .env) with ONLY that file's variables.
set -euo pipefail
ENV_FILE=${1:?usage: liquidity-local-boot.sh <testnet-env-file> [seconds]}
SECS=${2:-45}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
fail() { echo "liquidity-local-boot: refusing — $*" >&2; exit 2; }
case "$(basename "$ENV_FILE")" in .env|.env.*) fail "pass a testnet-only file, not $ENV_FILE";; esac
get() { (grep -E "^$1=" "$ENV_FILE" || true) | tail -1 | cut -d= -f2- | sed -e 's/^["'\'']//' -e 's/["'\'']$//'; }

[ "$(get ENV)" = development ] || fail "ENV must be development"
[ "$(get SONIC_NETWORK)" = testnet ] || fail "SONIC_NETWORK must be testnet"
[ "$(get BSC_NETWORK)" = testnet ] || fail "BSC_NETWORK must be testnet"
case "$(get RAMP_CONVERSION_BUY)" in ""|false|0) ;; *) fail "RAMP_CONVERSION_BUY must be off";; esac
for v in PAYCREST_API_KEY FLUTTERWAVE_CLIENT_ID FLUTTERWAVE_CLIENT_SECRET NOMBA_CLIENT_ID NOMBA_CLIENT_SECRET \
         MAILJET_API_KEY DIDIT_API_KEY IT_OPERATOR_PRIVATE_KEY RELAYER_PRIVATE_KEY RAMP_SPONSOR_PRIVATE_KEY \
         RAMP_TREASURY_WALLET_PRIVATE_KEY; do
  [ -z "$(get "$v")" ] || fail "$v must be unset (no live provider credentials, no other signer keys)"
done
DB=$(get DATABASE_URL)
[[ "$DB" =~ ^postgres(ql)?://[^@/]*@(localhost|127\.0\.0\.1)(:[0-9]+)?/tsa_liq_[a-z0-9_]+(\?.*)?$ ]] \
  || fail "DATABASE_URL must be a local throwaway database named tsa_liq_*"

BIN="$(mktemp -d)/liq-api"; RUN="$(mktemp -d)"; LOG="$RUN/boot.log"
(cd "$ROOT" && go build -o "$BIN" ./cmd/server)
env -i PATH="$PATH" HOME="$HOME" bash -c 'set -a; . "$1"; set +a; cd "$2"; exec "$3"' _ "$ENV_FILE" "$RUN" "$BIN" > "$LOG" 2>&1 &
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
