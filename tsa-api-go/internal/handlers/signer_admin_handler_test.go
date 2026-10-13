package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// adminNode is a chain for a registry lane: it accepts everything and has
// mined nonce 5.
type adminNode struct{ sent []*types.Transaction }

func (n *adminNode) ChainID() *big.Int { return big.NewInt(56) }
func (n *adminNode) SendRawTransaction(raw []byte) (string, error) {
	tx := new(types.Transaction)
	if err := rlp.DecodeBytes(raw, tx); err != nil {
		return "", err
	}
	n.sent = append(n.sent, tx)
	return tx.Hash().Hex(), nil
}
func (n *adminNode) LatestNonce(context.Context, string) (uint64, error) { return 5, nil }
func (n *adminNode) SuggestGasPrice() (*big.Int, error)                  { return big.NewInt(1e9), nil }

// The super-admin fill-gap route sends a 0-value self-transfer at the hole
// and writes an audit row naming the admin, the lane and the hash — never key
// material.
func TestSignerAdmin_FillGapSendsASelfTransferAndAudits(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(`CREATE TABLE p2p_audit_logs (id text primary key, actor_id text, trade_id text, ad_id text, action text, metadata text, created_at datetime)`)
	key, _ := crypto.GenerateKey()
	reg := services.NewSignerRegistry()
	node := &adminNode{}
	lane := reg.Lane("pm_release", key, node)
	// Our own call at 6 (the RPC said 6: something else held 5).
	if _, _, err := lane.Send(context.Background(), func() ([]byte, error) {
		return json.Marshal(blockchain.UnsignedTx{Nonce: 6, GasPrice: "1000000000", GasLimit: 21000,
			To: "0x00000000000000000000000000000000000000aa", Value: "0", ChainID: "56"})
	}); err != nil {
		t.Fatal(err)
	}
	h := &SignerAdminHandler{DB: db, Registry: reg}
	body, _ := json.Marshal(map[string]any{"chainId": 56, "address": lane.Address(), "nonce": 5})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/admin/signer/lanes/fill-gap", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	admin := models.User{ID: uuid.New()}
	c.Set("user", &admin)
	h.FillGap(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	last := node.sent[len(node.sent)-1]
	if last.Nonce() != 5 || last.Value().Sign() != 0 || !strings.EqualFold(last.To().Hex(), lane.Address()) {
		t.Fatalf("fill tx nonce=%d to=%s value=%s, want a 0-value self-transfer at 5", last.Nonce(), last.To(), last.Value())
	}
	var action, meta, actor string
	db.Raw(`SELECT action, metadata, actor_id FROM p2p_audit_logs`).Row().Scan(&action, &meta, &actor)
	if action != "signer.lane_gap_filled" || !strings.Contains(meta, last.Hash().Hex()) || actor != admin.ID.String() {
		t.Fatalf("audit = %s %s %s", action, meta, actor)
	}
	if strings.Contains(meta, "private") {
		t.Fatal("no key material in the audit row")
	}
	// Clearing a lane that is not held is a 409.
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	b2, _ := json.Marshal(map[string]any{"chainId": 56, "address": lane.Address()})
	c2.Request = httptest.NewRequest(http.MethodPost, "/api/admin/signer/lanes/clear-hold", bytes.NewReader(b2))
	c2.Request.Header.Set("Content-Type", "application/json")
	h.ClearHold(c2)
	if w2.Code != http.StatusConflict {
		t.Fatalf("clear-hold on an unheld lane = %d", w2.Code)
	}
}
