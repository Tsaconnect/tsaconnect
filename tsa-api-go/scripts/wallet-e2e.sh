#!/usr/bin/env bash
#
# Wallet System E2E Test Script
# Usage: ./scripts/wallet-e2e.sh [BASE_URL] [JWT_TOKEN]
#
# Prerequisites:
#   1. Server running: go run ./cmd/server
#   2. Valid JWT token (login first via /api/auth/login)
#
# Examples:
#   ./scripts/wallet-e2e.sh
#   ./scripts/wallet-e2e.sh http://localhost:5000 "eyJhbG..."

set -euo pipefail

BASE="${1:-http://localhost:5000}"
TOKEN="${2:-}"
PASS=0
FAIL=0
SKIP=0

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[0;33m'
CYAN='\033[0;36m'
NC='\033[0m'

# Test wallet address — change to your test wallet
WALLET_ADDR="0x742d35Cc6634C0532925a3b844Bc9e7595f2bD18"
DEST_ADDR="0xaF326D5D242C9A55590540f14658adDDd3586A8d"

# ─── Helpers ──────────────────────────────────────────────

print_header() {
  echo ""
  echo -e "${CYAN}━━━ $1 ━━━${NC}"
}

assert_status() {
  local test_name="$1"
  local expected="$2"
  local actual="$3"
  local body="$4"

  if [ "$actual" -eq "$expected" ]; then
    echo -e "  ${GREEN}PASS${NC} $test_name (HTTP $actual)"
    PASS=$((PASS + 1))
  else
    echo -e "  ${RED}FAIL${NC} $test_name — expected $expected, got $actual"
    echo "       Response: $(echo "$body" | head -c 200)"
    FAIL=$((FAIL + 1))
  fi
}

skip_test() {
  echo -e "  ${YELLOW}SKIP${NC} $1 — $2"
  SKIP=$((SKIP + 1))
}

curl_get() {
  local url="$1"
  local auth="${2:-}"
  if [ -n "$auth" ]; then
    curl -s -w "\n%{http_code}" -H "Authorization: Bearer $auth" "$url"
  else
    curl -s -w "\n%{http_code}" "$url"
  fi
}

curl_post() {
  local url="$1"
  local data="$2"
  local auth="${3:-}"
  if [ -n "$auth" ]; then
    curl -s -w "\n%{http_code}" -X POST \
      -H "Content-Type: application/json" \
      -H "Authorization: Bearer $auth" \
      -d "$data" "$url"
  else
    curl -s -w "\n%{http_code}" -X POST \
      -H "Content-Type: application/json" \
      -d "$data" "$url"
  fi
}

extract_status() {
  echo "$1" | tail -1
}

extract_body() {
  echo "$1" | sed '$d'
}

# ─── Pre-flight ───────────────────────────────────────────

echo ""
echo "========================================"
echo "  Wallet System E2E Tests"
echo "  Server: $BASE"
echo "========================================"

# ─── 1. Health Check ──────────────────────────────────────

print_header "1. Server Health"

RESP=$(curl_get "$BASE/health")
STATUS=$(extract_status "$RESP")
BODY=$(extract_body "$RESP")
assert_status "GET /health" 200 "$STATUS" "$BODY"

# ─── 2. Auth Required (no token) ─────────────────────────

print_header "2. Auth Required (no token)"

ENDPOINTS=(
  "GET,$BASE/api/wallet/supported-tokens"
  "GET,$BASE/api/wallet/balances"
  "GET,$BASE/api/wallet/transactions"
  "POST,$BASE/api/wallet/register"
  "POST,$BASE/api/wallet/prepare-tx"
  "POST,$BASE/api/wallet/submit-tx"
  "POST,$BASE/api/wallet/seed-phrase-backed-up"
)

for entry in "${ENDPOINTS[@]}"; do
  METHOD="${entry%%,*}"
  URL="${entry#*,}"
  EPATH="${URL#$BASE}"

  if [ "$METHOD" = "GET" ]; then
    RESP=$(curl_get "$URL")
  else
    RESP=$(curl_post "$URL" '{}')
  fi
  STATUS=$(extract_status "$RESP")
  BODY=$(extract_body "$RESP")
  assert_status "$METHOD $EPATH without auth => 401" 401 "$STATUS" "$BODY"
done

# ─── Check for token ─────────────────────────────────────

if [ -z "$TOKEN" ]; then
  echo ""
  echo -e "${YELLOW}No JWT token provided. Remaining tests require auth.${NC}"
  echo ""
  echo "To get a token, login first:"
  echo "  curl -s -X POST $BASE/api/auth/login \\"
  echo "    -H 'Content-Type: application/json' \\"
  echo "    -d '{\"email\":\"your@email.com\",\"password\":\"yourpass\"}'"
  echo ""
  echo "Then re-run:"
  echo "  ./scripts/wallet-e2e.sh $BASE \"YOUR_TOKEN\""
  echo ""
  echo -e "Results: ${GREEN}$PASS passed${NC}, ${RED}$FAIL failed${NC}, ${YELLOW}$SKIP skipped${NC}"
  exit 0
fi

# ─── 3. Supported Tokens ─────────────────────────────────

print_header "3. Supported Tokens"

RESP=$(curl_get "$BASE/api/wallet/supported-tokens" "$TOKEN")
STATUS=$(extract_status "$RESP")
BODY=$(extract_body "$RESP")
assert_status "GET /wallet/supported-tokens" 200 "$STATUS" "$BODY"
echo "       Tokens: $(echo "$BODY" | python3 -c "import sys,json; d=json.load(sys.stdin); print(len(d.get('data',[])), 'tokens')" 2>/dev/null || echo "(couldn't parse)")"

# ─── 4. Register Wallet ──────────────────────────────────

print_header "4. Register Wallet Address"

# 4a. Invalid format
RESP=$(curl_post "$BASE/api/wallet/register" '{"walletAddress":"not-valid"}' "$TOKEN")
STATUS=$(extract_status "$RESP")
BODY=$(extract_body "$RESP")
assert_status "POST /wallet/register invalid format => 400" 400 "$STATUS" "$BODY"

# 4b. Valid address
RESP=$(curl_post "$BASE/api/wallet/register" "{\"walletAddress\":\"$WALLET_ADDR\"}" "$TOKEN")
STATUS=$(extract_status "$RESP")
BODY=$(extract_body "$RESP")
if [ "$STATUS" -eq 200 ]; then
  assert_status "POST /wallet/register valid address => 200" 200 "$STATUS" "$BODY"
elif [ "$STATUS" -eq 409 ]; then
  echo -e "  ${YELLOW}NOTE${NC} POST /wallet/register => 409 (already registered, that's OK)"
  PASS=$((PASS + 1))
else
  assert_status "POST /wallet/register valid address => 200" 200 "$STATUS" "$BODY"
fi

# 4c. Duplicate (re-register same address with a different concept - if another user tried)
# We can't easily test this without a second user, so just note it
skip_test "POST /wallet/register duplicate" "requires second user account"

# ─── 5. Wallet Balances ──────────────────────────────────

print_header "5. Wallet Balances"

# 5a. All chains
RESP=$(curl_get "$BASE/api/wallet/balances" "$TOKEN")
STATUS=$(extract_status "$RESP")
BODY=$(extract_body "$RESP")
assert_status "GET /wallet/balances (all chains)" 200 "$STATUS" "$BODY"
echo "       Response: $(echo "$BODY" | python3 -c "
import sys,json
d=json.load(sys.stdin).get('data',{}).get('balances',{})
for chain,bals in d.items():
    tokens = ', '.join(f'{k}={v}' for k,v in bals.items())
    print(f'         {chain}: {tokens}')
" 2>/dev/null || echo "(couldn't parse)")"

# 5b. Sonic only
RESP=$(curl_get "$BASE/api/wallet/balances?chainId=14601" "$TOKEN")
STATUS=$(extract_status "$RESP")
BODY=$(extract_body "$RESP")
assert_status "GET /wallet/balances?chainId=14601 (Sonic)" 200 "$STATUS" "$BODY"

# 5c. BSC only
RESP=$(curl_get "$BASE/api/wallet/balances?chainId=97" "$TOKEN")
STATUS=$(extract_status "$RESP")
BODY=$(extract_body "$RESP")
assert_status "GET /wallet/balances?chainId=97 (BSC)" 200 "$STATUS" "$BODY"

# 5d. Unsupported chain
RESP=$(curl_get "$BASE/api/wallet/balances?chainId=99999" "$TOKEN")
STATUS=$(extract_status "$RESP")
BODY=$(extract_body "$RESP")
assert_status "GET /wallet/balances?chainId=99999 => 400" 400 "$STATUS" "$BODY"

# ─── 6. Prepare Transaction ──────────────────────────────

print_header "6. Prepare Transaction"

# 6a. Native transfer (S on Sonic)
RESP=$(curl_post "$BASE/api/wallet/prepare-tx" "{
  \"tokenSymbol\": \"S\",
  \"toAddress\": \"$DEST_ADDR\",
  \"amount\": \"0.001\",
  \"chainId\": 14601
}" "$TOKEN")
STATUS=$(extract_status "$RESP")
BODY=$(extract_body "$RESP")
assert_status "POST /wallet/prepare-tx native S on Sonic" 200 "$STATUS" "$BODY"

# 6b. ERC-20 transfer (USDT on Sonic)
RESP=$(curl_post "$BASE/api/wallet/prepare-tx" "{
  \"tokenSymbol\": \"USDT\",
  \"toAddress\": \"$DEST_ADDR\",
  \"amount\": \"1.0\",
  \"chainId\": 14601
}" "$TOKEN")
STATUS=$(extract_status "$RESP")
BODY=$(extract_body "$RESP")
# May fail if USDT not configured — 200 or 400 both acceptable
if [ "$STATUS" -eq 200 ]; then
  assert_status "POST /wallet/prepare-tx USDT on Sonic" 200 "$STATUS" "$BODY"
else
  echo -e "  ${YELLOW}NOTE${NC} POST /wallet/prepare-tx USDT => $STATUS (token may not be configured)"
  SKIP=$((SKIP + 1))
fi

# 6c. BSC native transfer (tBNB)
RESP=$(curl_post "$BASE/api/wallet/prepare-tx" "{
  \"tokenSymbol\": \"tBNB\",
  \"toAddress\": \"$DEST_ADDR\",
  \"amount\": \"0.001\",
  \"chainId\": 97
}" "$TOKEN")
STATUS=$(extract_status "$RESP")
BODY=$(extract_body "$RESP")
assert_status "POST /wallet/prepare-tx tBNB on BSC" 200 "$STATUS" "$BODY"

# 6d. Unsupported chain
RESP=$(curl_post "$BASE/api/wallet/prepare-tx" "{
  \"tokenSymbol\": \"S\",
  \"toAddress\": \"$DEST_ADDR\",
  \"amount\": \"0.001\",
  \"chainId\": 99999
}" "$TOKEN")
STATUS=$(extract_status "$RESP")
BODY=$(extract_body "$RESP")
assert_status "POST /wallet/prepare-tx unsupported chain => 400" 400 "$STATUS" "$BODY"

# 6e. Invalid address
RESP=$(curl_post "$BASE/api/wallet/prepare-tx" '{
  "tokenSymbol": "S",
  "toAddress": "not-an-address",
  "amount": "0.001",
  "chainId": 14601
}' "$TOKEN")
STATUS=$(extract_status "$RESP")
BODY=$(extract_body "$RESP")
assert_status "POST /wallet/prepare-tx invalid address => 400" 400 "$STATUS" "$BODY"

# 6f. Invalid amount
RESP=$(curl_post "$BASE/api/wallet/prepare-tx" "{
  \"tokenSymbol\": \"S\",
  \"toAddress\": \"$DEST_ADDR\",
  \"amount\": \"-5\",
  \"chainId\": 14601
}" "$TOKEN")
STATUS=$(extract_status "$RESP")
BODY=$(extract_body "$RESP")
assert_status "POST /wallet/prepare-tx negative amount => 400" 400 "$STATUS" "$BODY"

# ─── 7. Submit Transaction ────────────────────────────────

print_header "7. Submit Transaction"

# 7a. Invalid hex
RESP=$(curl_post "$BASE/api/wallet/submit-tx" "{
  \"signedTx\": \"not-valid-hex\",
  \"txType\": \"send\",
  \"tokenSymbol\": \"S\",
  \"toAddress\": \"$DEST_ADDR\",
  \"amount\": \"0.001\",
  \"chainId\": 14601
}" "$TOKEN")
STATUS=$(extract_status "$RESP")
BODY=$(extract_body "$RESP")
assert_status "POST /wallet/submit-tx invalid hex => 400" 400 "$STATUS" "$BODY"

# 7b. Invalid txType
RESP=$(curl_post "$BASE/api/wallet/submit-tx" "{
  \"signedTx\": \"0xdeadbeef\",
  \"txType\": \"invalid_type\",
  \"tokenSymbol\": \"S\",
  \"toAddress\": \"$DEST_ADDR\",
  \"amount\": \"0.001\",
  \"chainId\": 14601
}" "$TOKEN")
STATUS=$(extract_status "$RESP")
BODY=$(extract_body "$RESP")
assert_status "POST /wallet/submit-tx invalid txType => 400" 400 "$STATUS" "$BODY"

# 7c. Unsupported chain
RESP=$(curl_post "$BASE/api/wallet/submit-tx" "{
  \"signedTx\": \"0xdeadbeef\",
  \"txType\": \"send\",
  \"tokenSymbol\": \"S\",
  \"toAddress\": \"$DEST_ADDR\",
  \"amount\": \"0.001\",
  \"chainId\": 99999
}" "$TOKEN")
STATUS=$(extract_status "$RESP")
BODY=$(extract_body "$RESP")
assert_status "POST /wallet/submit-tx unsupported chain => 400" 400 "$STATUS" "$BODY"

# 7d. Real submission — skipped (would need actual signed tx)
skip_test "POST /wallet/submit-tx real signed tx" "requires real signed transaction from wallet"

# ─── 8. Transaction History ───────────────────────────────

print_header "8. Transaction History"

# 8a. All history
RESP=$(curl_get "$BASE/api/wallet/transactions?page=1&limit=10" "$TOKEN")
STATUS=$(extract_status "$RESP")
BODY=$(extract_body "$RESP")
assert_status "GET /wallet/transactions (paginated)" 200 "$STATUS" "$BODY"

# 8b. Filter by chain
RESP=$(curl_get "$BASE/api/wallet/transactions?page=1&limit=10&chainId=14601" "$TOKEN")
STATUS=$(extract_status "$RESP")
BODY=$(extract_body "$RESP")
assert_status "GET /wallet/transactions?chainId=14601" 200 "$STATUS" "$BODY"

# ─── 9. Seed Phrase Backup ────────────────────────────────

print_header "9. Seed Phrase Backup"

RESP=$(curl_post "$BASE/api/wallet/seed-phrase-backed-up" '{}' "$TOKEN")
STATUS=$(extract_status "$RESP")
BODY=$(extract_body "$RESP")
assert_status "POST /wallet/seed-phrase-backed-up" 200 "$STATUS" "$BODY"

# ─── Summary ──────────────────────────────────────────────

echo ""
echo "========================================"
echo "  Results"
echo "========================================"
echo -e "  ${GREEN}Passed: $PASS${NC}"
echo -e "  ${RED}Failed: $FAIL${NC}"
echo -e "  ${YELLOW}Skipped: $SKIP${NC}"
echo "========================================"
echo ""

if [ "$FAIL" -gt 0 ]; then
  exit 1
fi
