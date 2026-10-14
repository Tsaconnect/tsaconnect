package tron

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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

// approveServer answers the allowance read with `allowanceHex` and records
// whether an approve was built.
func approveServer(t *testing.T, allowanceHex string, built *map[string]any) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/wallet/triggerconstantcontract":
			w.Write([]byte(`{"result":{"result":true},"constant_result":["` + allowanceHex + `"]}`))
		case "/wallet/triggersmartcontract":
			_ = json.Unmarshal(body, built)
			w.Write([]byte(`{"result":{"result":true},"transaction":{"txID":"` + fixtureTxID + `","raw_data":{"contract":[]},"raw_data_hex":"0a02","visible":true}}`))
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

func TestPrepareApprove_AllowanceSufficientNeedsNoSignature(t *testing.T) {
	var built map[string]any
	srv := approveServer(t, "0000000000000000000000000000000000000000000000000000000002faf080", &built) // exactly 50 USDT
	defer srv.Close()
	prepared, err := newTestClient(srv.URL).PrepareTransfer(context.Background(), approveReq())
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

func TestPrepareApprove_BuildsMaxApproveWhenShort(t *testing.T) {
	var built map[string]any
	srv := approveServer(t, "0000000000000000000000000000000000000000000000000000000002faf07f", &built) // 1 short
	defer srv.Close()
	prepared, err := newTestClient(srv.URL).PrepareTransfer(context.Background(), approveReq())
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Payloads) != 1 || prepared.Payloads[0].HashHex != fixtureTxID {
		t.Fatalf("want one payload, got %+v", prepared.Payloads)
	}
	spender, _ := abiAddressWord(fixtureDiamond)
	if built["function_selector"] != "approve(address,uint256)" || built["contract_address"] != fixtureUSDT ||
		built["parameter"] != spender+strings.Repeat("f", 64) {
		t.Fatalf("unexpected approve call: %+v", built)
	}
}

// A route and an approval are contract calls: the preflight asks for the
// typical burn (not the fee_limit), skips the recipient lookup, and keeps the
// friendly shortfall note the prepare handler turns into a 402.
func TestEstimateFee_ContractCallPreflight(t *testing.T) {
	for _, bal := range []struct {
		sun  string
		want bool
	}{{"0", false}, {"10000000", true}} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasSuffix(r.URL.Path, fixtureOwner) {
				t.Errorf("only the sender should be looked up, got %s", r.URL.Path)
			}
			w.Write([]byte(`{"data":[{"balance":` + bal.sun + `,"trc20":[]}]}`))
		}))
		c := newTestClient(srv.URL)
		for _, req := range []chains.TransferRequest{fixtureSwapReq(fixtureRawHex(t)), approveReq()} {
			q, err := c.EstimateFee(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if q.HasEnoughGas != bal.want || q.NeedsActivation || q.EstFeeRaw != "10000000" {
				t.Fatalf("balance %s: unexpected quote %+v", bal.sun, q)
			}
			if !bal.want && q.Note != "You need TRX in this wallet to pay the Tron network fee." {
				t.Fatalf("missing shortfall note: %+v", q)
			}
		}
		srv.Close()
	}
}
