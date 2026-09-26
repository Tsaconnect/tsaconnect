package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// The app's activity feed asks for its Wallet group as one comma list
// (type=send,receive,approve,escrow). An exact match on that string found no
// rows, so the feed showed no EVM transfers at all.
func TestGetTransactionHistory_TypeFilter(t *testing.T) {
	db := setupTPTestDB(t)
	if err := db.Exec(`CREATE TABLE wallet_transactions (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL,
		tx_hash TEXT NOT NULL UNIQUE,
		token_symbol TEXT NOT NULL,
		tx_type TEXT NOT NULL,
		from_address TEXT NOT NULL,
		to_address TEXT NOT NULL,
		amount TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'pending',
		chain TEXT NOT NULL DEFAULT 'sonic',
		chain_id INTEGER NOT NULL DEFAULT 14601,
		block_number INTEGER,
		created_at DATETIME,
		updated_at DATETIME
	)`).Error; err != nil {
		t.Fatalf("create wallet_transactions: %v", err)
	}

	user := createTPTestUser(t, db, "Wally", "wally_hist", nil)
	for i, txType := range []string{"send", "receive", "approve", "escrow", "swap"} {
		tx := models.WalletTransaction{
			ID:          uuid.New(),
			UserID:      user.ID,
			TxHash:      "0x" + txType,
			TokenSymbol: "USDT",
			TxType:      txType,
			FromAddress: "0xfrom",
			ToAddress:   "0xto",
			Amount:      "1",
			Status:      "confirmed",
			CreatedAt:   time.Now().Add(time.Duration(i) * time.Minute),
		}
		if err := db.Create(&tx).Error; err != nil {
			t.Fatalf("create tx %s: %v", txType, err)
		}
	}

	h := &Handlers{}
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name  string
		query string
		want  []string
	}{
		{"single type", "type=swap", []string{"swap"}},
		{"comma list", "type=send,receive,approve,escrow", []string{"approve", "escrow", "receive", "send"}},
		{"list tolerates blanks and spaces", "type=send,+receive,", []string{"receive", "send"}},
		{"no filter", "", []string{"approve", "escrow", "receive", "send", "swap"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Set("user", &user)
			c.Request, _ = http.NewRequest("GET", "/wallet/transactions?"+tc.query, nil)

			h.GetTransactionHistory(c)

			if w.Code != http.StatusOK {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			var resp struct {
				Data struct {
					Transactions []models.WalletTransaction `json:"transactions"`
				} `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			got := make([]string, 0, len(resp.Data.Transactions))
			for _, tx := range resp.Data.Transactions {
				got = append(got, tx.TxType)
			}
			sort.Strings(got)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}
