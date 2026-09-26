package solana

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"testing"

	solanago "github.com/gagliardetto/solana-go"
	solanarpc "github.com/gagliardetto/solana-go/rpc"
	"github.com/ojimcy/tsa-api-go/internal/chains"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

// activity_test.go — the wallet activity feed, read from a REAL capture.
//
// activityFor is live for every Solana wallet whatever flags are set, and it
// changed under the ramp work in two ways the feed shows directly: an owner
// loaded through a v0 lookup table is found at all (before, only the static
// account keys were searched, and a loaded owner had no SOL row), and the
// owner's token accounts of one mint are summed. The capture is served over
// JSON-RPC exactly as RPC.Transaction asks for it, so the whole read path runs.

const v0FixtureSig = "4m6W5aKM5Vw15szsNAJotgtQtTWDYTRo4df1pj5froJeriRtRADDqH4hyUKMZjnKKwBeDcobn78b51a63dLVWxEz"

// serveTransaction answers every getTransaction with the named capture.
func serveTransaction(t *testing.T, fixture string) *RPC {
	t.Helper()
	result, err := os.ReadFile("testdata/" + fixture)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(body, &req); err != nil || req.Method != "getTransaction" {
			http.Error(w, "unexpected request: "+string(body), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":` + string(result) + `}`))
	}))
	t.Cleanup(srv.Close)
	return NewRPC(srv.URL)
}

func TestActivityFor_ReadsAV0LookupTableTransaction(t *testing.T) {
	c := NewClient(config.NonEVMChainConfig{NativeSymbol: "SOL"}, serveTransaction(t, "delivery_sol_v0_tx.json"))
	sig := &solanarpc.TransactionSignature{Signature: solanago.MustSignatureFromBase58(v0FixtureSig)}
	owner := solanago.MustPublicKeyFromBase58(v0FixtureOwner)

	got := c.activityFor(context.Background(), owner, sig)
	sort.Slice(got, func(i, j int) bool { return got[i].Symbol < got[j].Symbol })

	// The owner is loaded writable account #11 — past the 10 static keys —
	// credited 0.042097172 SOL, and not the fee payer, so no fee is taken off.
	// In the same transaction she sold 24,258,188.460268 of the pump token.
	want := []chains.Activity{
		{Hash: v0FixtureSig, Direction: "out", AmountRaw: "24258188460268", Decimals: 6,
			Symbol: "41iq…pump", Status: "confirmed", BlockTime: 1790160013},
		{Hash: v0FixtureSig, Direction: "in", AmountRaw: v0FixtureLamport, Decimals: solDecimals,
			Symbol: "SOL", Status: "confirmed", BlockTime: 1790160013},
	}
	if len(got) != len(want) {
		t.Fatalf("rows = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	// A wallet the transaction never touched gets no rows at all.
	stranger := solanago.MustPublicKeyFromBase58(fixtureStranger)
	if rows := c.activityFor(context.Background(), stranger, sig); len(rows) != 0 {
		t.Fatalf("someone else's feed shows %+v", rows)
	}
}
