package services

import (
	"reflect"

	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// bindTestEscrows makes the P2P trades and ads a test seeds live on their chain's escrow in reg,
// as the API records them at open and publish: a trade or ad seeded without an escrow address gets
// its chain's, and a trade without a key gets TradeKey(opener, HashID(id)). A test that sets
// either itself keeps its value. Without it every seeded row would read as a pre-switch row on a
// retired escrow (ErrRetiredEscrow).
func bindTestEscrows(db *gorm.DB, reg func() *P2PEscrowRegistry) {
	escrowOf := func(chainID int64) string {
		if esc, err := reg().For(chainID); err == nil {
			return esc.ContractAddress()
		}
		return ""
	}
	fill := func(v any) {
		switch row := v.(type) {
		case *models.P2PTrade:
			if row.EscrowAddress == "" {
				row.EscrowAddress = escrowOf(row.ChainID)
			}
			if row.OnChainTradeID == "" {
				row.OnChainTradeID = newTradeKeyHex(row.OpenerAddress, row.ID)
			}
		case *models.P2PAd:
			if row.EscrowAddress == "" {
				row.EscrowAddress = escrowOf(row.ChainID)
			}
		}
	}
	hook := func(tx *gorm.DB) {
		rv := tx.Statement.ReflectValue
		switch rv.Kind() {
		case reflect.Struct:
			if rv.CanAddr() {
				fill(rv.Addr().Interface())
			}
		case reflect.Slice, reflect.Array:
			for i := 0; i < rv.Len(); i++ {
				if el := rv.Index(i); el.CanAddr() {
					fill(el.Addr().Interface())
				}
			}
		}
	}
	const name = "test:p2p_escrow_defaults"
	if db.Callback().Create().Get(name) != nil {
		_ = db.Callback().Create().Replace(name, hook)
		return
	}
	_ = db.Callback().Create().After("gorm:before_create").Before("gorm:create").Register(name, hook)
}

// bindSvcEscrows is bindTestEscrows over svc's database and whatever registry svc holds at seed time.
func bindSvcEscrows(svc *P2PTradeService) *P2PTradeService {
	bindTestEscrows(svc.db, func() *P2PEscrowRegistry { return svc.escrows })
	return svc
}
