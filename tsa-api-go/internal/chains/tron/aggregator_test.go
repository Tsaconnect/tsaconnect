package tron

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"

	"github.com/ojimcy/tsa-api-go/internal/chains"
)

// The fixture is a live LiFi quote (2026-10-14): 50 USDT → TRX on SunSwap,
// customData.tronTransaction.raw_data_hex, for fromAddress fixtureOwner.
const (
	fixtureOwner   = "TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf"
	fixtureDiamond = "TU3ymitEKCWQFtASkEeHaPb8NfZcJtCHLt" // LiFi diamond, transactionRequest.to
	fixtureUSDT    = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
	fixtureTxID    = "6cf6707bcc454368143b72ce1bbc0bed511e650431d3eecfc9fca6af3f64c221"
	fixtureExpMs   = 1791977434160
	otherAddress   = "T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb"
)

// fixtureNow is a minute before the fixture's expiration.
var fixtureNow = time.UnixMilli(fixtureExpMs - 60_000)

func fixtureRawHex(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/lifi_usdt_trx_raw.hex")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func fixtureSwapReq(rawHex string) chains.TransferRequest {
	return chains.TransferRequest{
		From: fixtureOwner, To: fixtureDiamond, Amount: "50000000", Symbol: "USDT",
		TokenAddr: fixtureUSDT, TronRawHex: rawHex,
	}
}

func addrBytes(t *testing.T, addr string) []byte {
	t.Helper()
	if !IsValidBase58(addr) {
		t.Fatalf("test address %s is not valid", addr)
	}
	h, _ := Base58ToHex(addr)
	b, _ := hex.DecodeString(h)
	return b
}

// synthContract describes one contract for encodeRaw.
type synthContract struct {
	typ        uint64
	typeURL    string // defaults to the type's own
	permission uint64
	owner      []byte
	target     []byte
	value      int64
	data       []byte
}

// encodeRaw builds a TransactionRaw the way java-tron serializes one.
func encodeRaw(expirationMs, feeLimit int64, contracts ...synthContract) []byte {
	b := protowire.AppendTag(nil, 1, protowire.BytesType)
	b = protowire.AppendBytes(b, []byte{0x0f, 0x37})
	b = protowire.AppendTag(b, 4, protowire.BytesType)
	b = protowire.AppendBytes(b, []byte{1, 2, 3, 4, 5, 6, 7, 8})
	b = protowire.AppendTag(b, 8, protowire.VarintType)
	b = protowire.AppendVarint(b, uint64(expirationMs))
	for _, c := range contracts {
		var v []byte
		v = protowire.AppendTag(v, 1, protowire.BytesType)
		v = protowire.AppendBytes(v, c.owner)
		v = protowire.AppendTag(v, 2, protowire.BytesType)
		v = protowire.AppendBytes(v, c.target)
		if c.value != 0 {
			v = protowire.AppendTag(v, 3, protowire.VarintType)
			v = protowire.AppendVarint(v, uint64(c.value))
		}
		if len(c.data) > 0 {
			v = protowire.AppendTag(v, 4, protowire.BytesType)
			v = protowire.AppendBytes(v, c.data)
		}
		url := c.typeURL
		if url == "" {
			url = acceptedContractTypes[c.typ]
			if url == "" {
				url = typeURLPrefix + "AccountPermissionUpdateContract"
			}
		}
		var any []byte
		any = protowire.AppendTag(any, 1, protowire.BytesType)
		any = protowire.AppendBytes(any, []byte(url))
		any = protowire.AppendTag(any, 2, protowire.BytesType)
		any = protowire.AppendBytes(any, v)
		var ct []byte
		ct = protowire.AppendTag(ct, 1, protowire.VarintType)
		ct = protowire.AppendVarint(ct, c.typ)
		ct = protowire.AppendTag(ct, 2, protowire.BytesType)
		ct = protowire.AppendBytes(ct, any)
		if c.permission != 0 {
			ct = protowire.AppendTag(ct, 5, protowire.VarintType)
			ct = protowire.AppendVarint(ct, c.permission)
		}
		b = protowire.AppendTag(b, 11, protowire.BytesType)
		b = protowire.AppendBytes(b, ct)
	}
	b = protowire.AppendTag(b, 14, protowire.VarintType)
	b = protowire.AppendVarint(b, uint64(expirationMs-600_000))
	if feeLimit >= 0 {
		b = protowire.AppendTag(b, 18, protowire.VarintType)
		b = protowire.AppendVarint(b, uint64(feeLimit))
	}
	return b
}

func TestDecodeTransactionRaw_LiFiFixture(t *testing.T) {
	raw, err := hex.DecodeString(fixtureRawHex(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := sha256.Sum256(raw); hex.EncodeToString(got[:]) != fixtureTxID {
		t.Fatalf("fixture txID mismatch")
	}
	d, err := decodeTransactionRaw(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(d.Contracts) != 1 || d.Contracts[0].Type != contractTypeTriggerSmartContract {
		t.Fatalf("want one TriggerSmartContract, got %+v", d.Contracts)
	}
	c := d.Contracts[0]
	if HexToBase58(hex.EncodeToString(c.Owner)) != fixtureOwner || HexToBase58(hex.EncodeToString(c.Target)) != fixtureDiamond {
		t.Fatalf("owner/target decoded wrong: %x %x", c.Owner, c.Target)
	}
	if d.ExpirationMs != fixtureExpMs || !d.HasFeeLimit || d.FeeLimit != 150_000_000 {
		t.Fatalf("expiration/fee_limit decoded wrong: %+v", d)
	}
}

func TestPrepareFromRawHex_AcceptsLiFiFixture(t *testing.T) {
	c := newTestClient("http://unused")
	c.now = func() time.Time { return fixtureNow }
	// LiFi's transactionRequest.data carries the same bytes with a 0x prefix.
	prepared, err := c.PrepareTransfer(context.Background(), fixtureSwapReq("0x"+fixtureRawHex(t)))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if prepared.Family != "tron" || len(prepared.Payloads) != 1 || prepared.Payloads[0].HashHex != fixtureTxID ||
		prepared.Payloads[0].Curve != chains.CurveSecp256k1 {
		t.Fatalf("unexpected prepared tx: %+v", prepared)
	}
	var raw RawTx
	if err := json.Unmarshal([]byte(prepared.Raw), &raw); err != nil {
		t.Fatal(err)
	}
	if raw.TxID != fixtureTxID || raw.RawHex != fixtureRawHex(t) || !isAggregatorRaw(&raw) {
		t.Fatalf("raw should carry the bytes and no raw_data JSON: %+v", raw)
	}
}

func TestValidateAggregatorRaw_Refusals(t *testing.T) {
	t.Parallel()
	owner, diamond, other := addrBytes(t, fixtureOwner), addrBytes(t, fixtureDiamond), addrBytes(t, otherAddress)
	usdt := addrBytes(t, fixtureUSDT)
	swapData, _ := hex.DecodeString("2c57e884" + strings.Repeat("00", 64))
	future := int64(fixtureExpMs)
	trigger := func(mod func(*synthContract)) synthContract {
		c := synthContract{typ: contractTypeTriggerSmartContract, owner: owner, target: diamond, data: swapData}
		if mod != nil {
			mod(&c)
		}
		return c
	}
	tokenReq := fixtureSwapReq("")
	trxReq := chains.TransferRequest{From: fixtureOwner, To: fixtureDiamond, Amount: "50000000", Symbol: "TRX"}

	cases := []struct {
		name string
		raw  []byte
		req  chains.TransferRequest
		ok   bool
	}{
		{"synthetic token swap passes", encodeRaw(future, 150_000_000, trigger(nil)), tokenReq, true},
		{"native swap within amount passes", encodeRaw(future, 150_000_000, trigger(func(c *synthContract) { c.value = 50_000_000 })), trxReq, true},
		{"plain TRX transfer passes", encodeRaw(future, -1, synthContract{typ: contractTypeTransfer, owner: owner, target: diamond, value: 1}), trxReq, true},

		{"wrong owner", encodeRaw(future, 150_000_000, trigger(func(c *synthContract) { c.owner = other })), tokenReq, false},
		{"two contracts", encodeRaw(future, 150_000_000, trigger(nil), trigger(nil)), tokenReq, false},
		{"no contract", encodeRaw(future, 150_000_000), tokenReq, false},
		{"permission update", encodeRaw(future, 150_000_000, synthContract{typ: 46, owner: owner, target: other}), tokenReq, false},
		{"type url mismatch", encodeRaw(future, 150_000_000, trigger(func(c *synthContract) { c.typeURL = typeURLPrefix + "TransferContract" })), tokenReq, false},
		{"non-owner permission", encodeRaw(future, 150_000_000, trigger(func(c *synthContract) { c.permission = 2 })), tokenReq, false},
		{"fee limit too high", encodeRaw(future, maxAggregatorFeeLimitSun+1, trigger(nil)), tokenReq, false},
		{"no fee limit on a call", encodeRaw(future, -1, trigger(nil)), tokenReq, false},
		{"expired", encodeRaw(fixtureNow.UnixMilli()-1, 150_000_000, trigger(nil)), tokenReq, false},
		{"different contract than the route names", encodeRaw(future, 150_000_000, trigger(func(c *synthContract) { c.target = other })), tokenReq, false},
		{"TRX attached to a token swap", encodeRaw(future, 150_000_000, trigger(func(c *synthContract) { c.value = 1 })), tokenReq, false},
		{"native swap over the amount", encodeRaw(future, 150_000_000, trigger(func(c *synthContract) { c.value = 50_000_001 })), trxReq, false},
		{"TRX transfer over the amount", encodeRaw(future, -1, synthContract{typ: contractTypeTransfer, owner: owner, target: diamond, value: 50_000_001}), trxReq, false},
		{"TRX transfer on a token swap", encodeRaw(future, -1, synthContract{typ: contractTypeTransfer, owner: owner, target: diamond, value: 1}), tokenReq, false},
		{"calls the token directly", encodeRaw(future, 150_000_000, trigger(func(c *synthContract) { c.target = usdt })),
			func() chains.TransferRequest { r := tokenReq; r.To = fixtureUSDT; return r }(), false},
		{"direct token transfer selector", encodeRaw(future, 150_000_000, trigger(func(c *synthContract) {
			c.data, _ = hex.DecodeString("a9059cbb" + strings.Repeat("00", 64))
		})), tokenReq, false},
	}
	for _, tc := range cases {
		d, err := decodeTransactionRaw(tc.raw)
		if err == nil {
			err = validateAggregatorRaw(d, tc.req, fixtureNow)
		}
		if tc.ok {
			if err != nil {
				t.Errorf("%s: want accepted, got %v", tc.name, err)
			}
			continue
		}
		var invalid *chains.InvalidRequestError
		if err == nil {
			t.Errorf("%s: want refused, got accepted", tc.name)
		} else if d != nil && !errors.As(err, &invalid) {
			t.Errorf("%s: want chains.InvalidRequestError, got %T (%v)", tc.name, err, err)
		} else {
			t.Logf("%s: %v", tc.name, err)
		}
	}
}

// Fields we cannot interpret (auths, scripts, anything newer) are refused, and
// prepare reports it as the route's fault.
func TestPrepareFromRawHex_RefusesUnknownField(t *testing.T) {
	raw, _ := hex.DecodeString(fixtureRawHex(t))
	raw = protowire.AppendTag(raw, 12, protowire.BytesType) // scripts
	raw = protowire.AppendBytes(raw, []byte{0x01})
	c := newTestClient("http://unused")
	c.now = func() time.Time { return fixtureNow }
	_, err := c.PrepareTransfer(context.Background(), fixtureSwapReq(hex.EncodeToString(raw)))
	var invalid *chains.InvalidRequestError
	if !errors.As(err, &invalid) {
		t.Fatalf("want chains.InvalidRequestError, got %T (%v)", err, err)
	}
}

func TestPrepare_RefusesApproveWithRawHex(t *testing.T) {
	c := newTestClient("http://unused")
	req := fixtureSwapReq(fixtureRawHex(t))
	req.Approve = true
	_, err := c.PrepareTransfer(context.Background(), req)
	var invalid *chains.InvalidRequestError
	if !errors.As(err, &invalid) {
		t.Fatalf("want chains.InvalidRequestError, got %T (%v)", err, err)
	}
}

// The aggregator path broadcasts protocol.Transaction{raw_data, signature} as
// hex — the exact validated bytes — and never the raw_data JSON route.
func TestAssembleAndBroadcast_AggregatorUsesBroadcastHex(t *testing.T) {
	sig := strings.Repeat("ab", 64) + "1b"
	var gotTx string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/wallet/broadcasthex" {
			t.Errorf("unexpected call %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var in struct {
			Transaction string `json:"transaction"`
		}
		_ = json.Unmarshal(body, &in)
		gotTx = in.Transaction
		w.Write([]byte(`{"result":true,"code":"SUCCESS","txid":"` + fixtureTxID + `"}`))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	c.now = func() time.Time { return fixtureNow }
	prepared, err := c.PrepareTransfer(context.Background(), fixtureSwapReq(fixtureRawHex(t)))
	if err != nil {
		t.Fatal(err)
	}
	hash, err := c.AssembleAndBroadcast(context.Background(), prepared, []chains.Signature{{Index: 0, SigHex: sig}})
	if err != nil {
		t.Fatalf("broadcast: %v", err)
	}
	if hash != fixtureTxID {
		t.Fatalf("hash = %s, want %s", hash, fixtureTxID)
	}
	raw, _ := hex.DecodeString(fixtureRawHex(t))
	sigBytes, _ := hex.DecodeString(sig)
	want := protowire.AppendTag(nil, 1, protowire.BytesType)
	want = protowire.AppendBytes(want, raw)
	want = protowire.AppendTag(want, 2, protowire.BytesType)
	want = protowire.AppendBytes(want, sigBytes)
	if gotTx != hex.EncodeToString(want) {
		t.Fatalf("broadcast bytes differ from Transaction{raw, sig}")
	}
}

// A prepared tx comes back from the client; bytes that no longer hash to the
// signed txID must never reach the network.
func TestAssembleAndBroadcast_AggregatorRefusesTamperedBytes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("nothing may be broadcast, got %s", r.URL.Path)
	}))
	defer srv.Close()
	c := newTestClient(srv.URL)
	rawHex := fixtureRawHex(t)
	tampered := rawHex[:len(rawHex)-2] + "48"
	rawJSON, _ := json.Marshal(RawTx{TxID: fixtureTxID, RawHex: tampered})
	prepared := chains.PreparedTx{Family: "tron", Raw: string(rawJSON)}
	_, err := c.AssembleAndBroadcast(context.Background(), prepared, []chains.Signature{{SigHex: strings.Repeat("ab", 65)}})
	if err == nil {
		t.Fatal("want refusal of bytes that do not match the txID")
	}
}

// approveServer answers the allowance read with `allowanceHex` and builds the
// approve it is asked for as a real TransactionRaw. tamper, when set, rewrites
// the calldata it returns — a node that builds something other than asked.
func approveServer(t *testing.T, allowanceHex string, built *map[string]any, tamper func(data []byte) []byte) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/wallet/triggerconstantcontract":
			w.Write([]byte(`{"result":{"result":true},"constant_result":["` + allowanceHex + `"]}`))
		case "/wallet/triggersmartcontract":
			_ = json.Unmarshal(body, built)
			in := *built
			data, _ := hex.DecodeString(approveSelector + in["parameter"].(string))
			if tamper != nil {
				data = tamper(data)
			}
			raw := encodeRaw(fixtureExpMs, trc20FeeLimitSun, synthContract{
				typ:   contractTypeTriggerSmartContract,
				owner: addrBytes(t, in["owner_address"].(string)), target: addrBytes(t, in["contract_address"].(string)),
				data: data,
			})
			sum := sha256.Sum256(raw)
			out, _ := json.Marshal(map[string]any{
				"result": map[string]any{"result": true},
				"transaction": map[string]any{
					"txID": hex.EncodeToString(sum[:]), "raw_data": map[string]any{"contract": []any{}},
					"raw_data_hex": hex.EncodeToString(raw), "visible": true,
				},
			})
			w.Write(out)
		default:
			t.Errorf("unexpected call %s", r.URL.Path)
		}
	}))
}

func approveReq() chains.TransferRequest {
	return chains.TransferRequest{
		From: fixtureOwner, To: fixtureDiamond, Amount: "50000000", Symbol: "USDT",
		TokenAddr: fixtureUSDT, Approve: true,
	}
}

func approveClient(url string) *Client {
	c := newTestClient(url)
	c.now = func() time.Time { return fixtureNow }
	return c
}

func TestPrepareApprove_AllowanceSufficientNeedsNoSignature(t *testing.T) {
	var built map[string]any
	srv := approveServer(t, "0000000000000000000000000000000000000000000000000000000002faf080", &built, nil) // exactly 50 USDT
	defer srv.Close()
	prepared, err := approveClient(srv.URL).PrepareTransfer(context.Background(), approveReq())
	if err != nil {
		t.Fatal(err)
	}
	if built != nil {
		t.Fatal("no approve may be built when the allowance already covers the amount")
	}
	// The app reads an empty payload list as "already approved"; it must be
	// [] on the wire, not null.
	out, _ := json.Marshal(prepared)
	if !strings.Contains(string(out), `"payloads":[]`) || prepared.Family != "tron" {
		t.Fatalf("want empty payloads, got %s", out)
	}
}

// The approval is exact, never unlimited: the route's token amount sits in
// calldata we cannot read, so the allowance is the only bound on it. A non-zero
// allowance below the amount is reset to 0 first (USDT-style tokens refuse
// non-zero → non-zero); the app's next round then approves the amount.
func TestPrepareApprove_ExactAmountAndResetFirst(t *testing.T) {
	spender, _ := abiAddressWord(fixtureDiamond)
	cases := []struct {
		name, allowance, wantAmountWord string
	}{
		{"from zero approves the amount", strings.Repeat("0", 64), fmt.Sprintf("%064x", 50_000_000)},
		{"short non-zero resets to 0", fmt.Sprintf("%064x", 50_000_000-1), strings.Repeat("0", 64)},
	}
	for _, tc := range cases {
		var built map[string]any
		srv := approveServer(t, tc.allowance, &built, nil)
		prepared, err := approveClient(srv.URL).PrepareTransfer(context.Background(), approveReq())
		srv.Close()
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if built["function_selector"] != "approve(address,uint256)" || built["contract_address"] != fixtureUSDT ||
			built["parameter"] != spender+tc.wantAmountWord {
			t.Fatalf("%s: unexpected approve call: %+v", tc.name, built)
		}
		var raw RawTx
		_ = json.Unmarshal([]byte(prepared.Raw), &raw)
		if len(prepared.Payloads) != 1 || prepared.Payloads[0].HashHex != raw.TxID || !isAggregatorRaw(&raw) {
			t.Fatalf("%s: want one payload over verified hex only, got %+v / %s", tc.name, prepared.Payloads, prepared.Raw)
		}
	}
}

// What TronGrid hands back is decoded before its txID is signed: an approve
// for a different amount or spender, or a different call altogether, is
// refused.
func TestPrepareApprove_RefusesNodeBuiltSomethingElse(t *testing.T) {
	other, _ := abiAddressWord(otherAddress)
	tampers := map[string]func([]byte) []byte{
		"unlimited amount": func(d []byte) []byte {
			copy(d[36:], bytesRepeat(0xff, 32))
			return d
		},
		"other spender": func(d []byte) []byte {
			w, _ := hex.DecodeString(other)
			copy(d[4:36], w)
			return d
		},
		"transfer instead": func(d []byte) []byte {
			copy(d[:4], []byte{0xa9, 0x05, 0x9c, 0xbb})
			return d
		},
	}
	for name, tamper := range tampers {
		var built map[string]any
		srv := approveServer(t, strings.Repeat("0", 64), &built, tamper)
		_, err := approveClient(srv.URL).PrepareTransfer(context.Background(), approveReq())
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), "unexpected approval") {
			t.Fatalf("%s: want refusal, got %v", name, err)
		}
	}
}

func bytesRepeat(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

// The spender and the route target must be configured aggregator contracts:
// the quote names both itself, so it cannot be what vouches for them.
func TestRouterAllowlist_RefusesUnlistedContract(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("nothing may be asked of TronGrid, got %s", r.URL.Path)
	}))
	defer srv.Close()
	c := approveClient(srv.URL)
	approve := approveReq()
	approve.To = otherAddress
	swap := fixtureSwapReq(fixtureRawHex(t))
	swap.To = otherAddress
	for name, req := range map[string]chains.TransferRequest{"approve": approve, "swap": swap} {
		_, err := c.PrepareTransfer(context.Background(), req)
		var invalid *chains.InvalidRequestError
		if !errors.As(err, &invalid) || !strings.Contains(err.Error(), "not a swap contract") {
			t.Fatalf("%s: want allowlist refusal, got %v", name, err)
		}
	}
	// An unconfigured network signs no route at all.
	c.cfg.Routers = nil
	if _, err := c.PrepareTransfer(context.Background(), fixtureSwapReq(fixtureRawHex(t))); err == nil {
		t.Fatal("an empty allowlist must refuse every route")
	}
}

// A route and an approval are contract calls: the preflight asks for the
// typical burn (not the fee_limit), skips the recipient lookup, and keeps the
// friendly shortfall note the prepare handler turns into a 402. A TRX-source
// route also has to hold the amount it spends.
func TestEstimateFee_ContractCallPreflight(t *testing.T) {
	trxSwap := chains.TransferRequest{From: fixtureOwner, To: fixtureDiamond, Amount: "50000000", Symbol: "TRX", TronRawHex: "0a02"}
	cases := []struct {
		name    string
		balance string
		req     chains.TransferRequest
		want    bool
		note    string
	}{
		{"token swap, no TRX", "0", fixtureSwapReq("0a02"), false, "You need TRX in this wallet to pay the Tron network fee."},
		{"token swap, fee covered", "10000000", fixtureSwapReq("0a02"), true, ""},
		{"approve, no TRX", "0", approveReq(), false, "You need TRX in this wallet to pay the Tron network fee."},
		{"approve, fee covered", "10000000", approveReq(), true, ""},
		{"TRX swap, fee only", "10000000", trxSwap, false, "Not enough TRX to cover the amount plus the Tron network fee."},
		{"TRX swap, amount + fee", "60000000", trxSwap, true, ""},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasSuffix(r.URL.Path, fixtureOwner) {
				t.Errorf("only the sender should be looked up, got %s", r.URL.Path)
			}
			w.Write([]byte(`{"data":[{"balance":` + tc.balance + `,"trc20":[]}]}`))
		}))
		q, err := newTestClient(srv.URL).EstimateFee(context.Background(), tc.req)
		srv.Close()
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if q.HasEnoughGas != tc.want || q.NeedsActivation || q.EstFeeRaw != "10000000" || q.Note != tc.note {
			t.Fatalf("%s: unexpected quote %+v", tc.name, q)
		}
	}
}

// Addresses are interpolated into TronGrid URL paths, so an invalid one never
// reaches the network.
func TestEstimateFee_ValidatesAddressesBeforeAnyLookup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("no lookup expected, got %s", r.URL.Path)
	}))
	defer srv.Close()
	c := newTestClient(srv.URL)
	for name, req := range map[string]chains.TransferRequest{
		"bad from, swap":     {From: "../wallet/x", To: fixtureDiamond, Amount: "1", TronRawHex: "0a02"},
		"bad from, transfer": {From: "../wallet/x", To: otherAddress, Amount: "1"},
		"bad to, transfer":   {From: fixtureOwner, To: "../wallet/x", Amount: "1"},
	} {
		_, err := c.EstimateFee(context.Background(), req)
		var invalid *chains.InvalidRequestError
		if !errors.As(err, &invalid) {
			t.Fatalf("%s: want chains.InvalidRequestError, got %v", name, err)
		}
	}
}

// pbField is one hand-written protobuf field, for bytes no honest encoder makes.
type pbField struct {
	num   protowire.Number
	typ   protowire.Type
	v     uint64
	bs    []byte
	extra []byte // raw bytes appended after the tag instead of v/bs
}

func pbMessage(fields ...pbField) []byte {
	var b []byte
	for _, f := range fields {
		b = protowire.AppendTag(b, f.num, f.typ)
		switch {
		case f.extra != nil:
			b = append(b, f.extra...)
		case f.typ == protowire.VarintType:
			b = protowire.AppendVarint(b, f.v)
		default:
			b = protowire.AppendBytes(b, f.bs)
		}
	}
	return b
}

func vf(num protowire.Number, v uint64) pbField {
	return pbField{num: num, typ: protowire.VarintType, v: v}
}
func bf(num protowire.Number, bs []byte) pbField {
	return pbField{num: num, typ: protowire.BytesType, bs: bs}
}

// wrapTrigger puts a TriggerSmartContract value into a Contract (whose own
// fields are ctFields, type and parameter first) and that into a TransactionRaw.
func wrapTrigger(value []byte, ctExtra ...pbField) []byte {
	anyMsg := pbMessage(bf(1, []byte(acceptedContractTypes[contractTypeTriggerSmartContract])), bf(2, value))
	ct := pbMessage(append([]pbField{vf(1, contractTypeTriggerSmartContract), bf(2, anyMsg)}, ctExtra...)...)
	return pbMessage(bf(1, []byte{0x0f, 0x37}), vf(8, uint64(fixtureExpMs)), bf(11, ct), vf(18, 150_000_000))
}

// Each trick below makes java-tron read a different transaction than a lenient
// decoder does — most often by sending a known field twice or under another
// wire type, so the value we check is not the value the node executes. The
// decoder must accept only the one canonical encoding.
func TestDecodeTransactionRaw_RefusesWireTricks(t *testing.T) {
	owner, diamond := addrBytes(t, fixtureOwner), addrBytes(t, fixtureDiamond)
	swapData, _ := hex.DecodeString("2c57e884" + strings.Repeat("00", 32))
	head := []pbField{bf(1, owner), bf(2, diamond)}
	trig := func(fields ...pbField) []byte {
		return wrapTrigger(pbMessage(append(append([]pbField{}, head...), fields...)...))
	}

	// The honest shape decodes, so every refusal below is the trick's doing.
	if _, err := decodeTransactionRaw(trig(vf(3, 5_000_000), bf(4, swapData))); err != nil {
		t.Fatalf("baseline must decode: %v", err)
	}
	tricks := map[string][]byte{
		"call_value then field 3 as bytes": trig(vf(3, 5_000_000), bf(3, []byte{1}), bf(4, swapData)),
		"call_value twice":                 trig(vf(3, 5_000_000), vf(3, 1), bf(4, swapData)),
		"data then field 4 as varint":      trig(bf(4, swapData), vf(4, 1)),
		"call_token_value as bytes":        trig(bf(4, swapData), bf(5, []byte{1})),
		"token_id as bytes":                trig(bf(4, swapData), bf(6, []byte{1})),
		"owner twice":                      wrapTrigger(pbMessage(bf(1, owner), bf(1, owner), bf(2, diamond))),
		"unknown trigger field":            trig(bf(4, swapData), vf(7, 1)),
		"out of order":                     wrapTrigger(pbMessage(bf(2, diamond), bf(1, owner))),
		"explicit zero call_value":         trig(vf(3, 0), bf(4, swapData)),
		"non-minimal varint":               trig(pbField{num: 3, typ: protowire.VarintType, extra: []byte{0x81, 0x00}}),
		"permission_id as bytes":           wrapTrigger(pbMessage(head...), bf(5, []byte{2})),
		"permission_id twice":              wrapTrigger(pbMessage(head...), vf(5, 2), vf(5, 3)),
		"unknown contract field":           wrapTrigger(pbMessage(head...), vf(6, 1)),
		"contract type twice": pbMessage(vf(8, uint64(fixtureExpMs)), bf(11, pbMessage(
			vf(1, contractTypeTriggerSmartContract), vf(1, 46),
			bf(2, pbMessage(bf(1, []byte(acceptedContractTypes[contractTypeTriggerSmartContract])), bf(2, pbMessage(head...))))))),
		"contract type as bytes": pbMessage(vf(8, uint64(fixtureExpMs)), bf(11, pbMessage(
			bf(1, []byte{31}),
			bf(2, pbMessage(bf(1, []byte(acceptedContractTypes[contractTypeTriggerSmartContract])), bf(2, pbMessage(head...))))))),
		"unknown parameter field": pbMessage(vf(8, uint64(fixtureExpMs)), bf(11, pbMessage(vf(1, contractTypeTriggerSmartContract),
			bf(2, pbMessage(bf(1, []byte(acceptedContractTypes[contractTypeTriggerSmartContract])), bf(2, pbMessage(head...)), vf(3, 1)))))),
		"transfer amount twice": pbMessage(vf(8, uint64(fixtureExpMs)), bf(11, pbMessage(vf(1, contractTypeTransfer),
			bf(2, pbMessage(bf(1, []byte(acceptedContractTypes[contractTypeTransfer])), bf(2, pbMessage(bf(1, owner), bf(2, diamond), vf(3, 1), vf(3, 9_000_000)))))))),
		"transfer amount as bytes": pbMessage(vf(8, uint64(fixtureExpMs)), bf(11, pbMessage(vf(1, contractTypeTransfer),
			bf(2, pbMessage(bf(1, []byte(acceptedContractTypes[contractTypeTransfer])), bf(2, pbMessage(bf(1, owner), bf(2, diamond), bf(3, []byte{1})))))))),
		"expiration twice": pbMessage(vf(8, uint64(fixtureExpMs)), vf(8, uint64(fixtureExpMs)+1)),
		"fee_limit twice":  append(trig(bf(4, swapData)), pbMessage(vf(18, 1))...),
		"fee_limit as bytes": pbMessage(vf(8, uint64(fixtureExpMs)), bf(11, pbMessage(vf(1, contractTypeTriggerSmartContract),
			bf(2, pbMessage(bf(1, []byte(acceptedContractTypes[contractTypeTriggerSmartContract])), bf(2, pbMessage(head...)))))), bf(18, []byte{1})),
		"auths field": append(trig(bf(4, swapData)), pbMessage(bf(19, []byte{1}))...),
	}
	for name, raw := range tricks {
		if _, err := decodeTransactionRaw(raw); err == nil {
			t.Errorf("%s: want refused, got decoded", name)
		} else {
			t.Logf("%s: %v", name, err)
		}
	}
}

// LiFi expires its transactions 10 minutes out; one signed to live much longer
// can be held back and broadcast after its price is gone.
func TestValidateAggregatorRaw_RefusesFarExpiry(t *testing.T) {
	owner, diamond := addrBytes(t, fixtureOwner), addrBytes(t, fixtureDiamond)
	swapData, _ := hex.DecodeString("2c57e884" + strings.Repeat("00", 32))
	c := synthContract{typ: contractTypeTriggerSmartContract, owner: owner, target: diamond, data: swapData}
	for _, tc := range []struct {
		exp time.Time
		ok  bool
	}{{fixtureNow.Add(14 * time.Minute), true}, {fixtureNow.Add(16 * time.Minute), false}} {
		d, err := decodeTransactionRaw(encodeRaw(tc.exp.UnixMilli(), 150_000_000, c))
		if err != nil {
			t.Fatal(err)
		}
		err = validateAggregatorRaw(d, fixtureSwapReq(""), fixtureNow)
		if (err == nil) != tc.ok {
			t.Fatalf("expiry %v: ok=%v, got %v", tc.exp.Sub(fixtureNow), tc.ok, err)
		}
	}
}

func TestValidateAggregatorRaw_RefusesMoreTokenSelectors(t *testing.T) {
	owner, diamond := addrBytes(t, fixtureOwner), addrBytes(t, fixtureDiamond)
	for _, sel := range []string{"d73dd623", "cae9ca51", "4000aea0", "1296ee62"} {
		data, _ := hex.DecodeString(sel + strings.Repeat("00", 64))
		d, err := decodeTransactionRaw(encodeRaw(fixtureExpMs, 150_000_000,
			synthContract{typ: contractTypeTriggerSmartContract, owner: owner, target: diamond, data: data}))
		if err != nil {
			t.Fatal(err)
		}
		if err := validateAggregatorRaw(d, fixtureSwapReq(""), fixtureNow); err == nil {
			t.Fatalf("selector %s: want refused", sel)
		}
	}
}
