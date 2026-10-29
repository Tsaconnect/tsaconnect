package services

import (
	"context"
	"errors"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// P2PParty is the operator's view of one trade party. Deliberately narrow: no
// email, phone or BVN.
type P2PParty struct {
	ID                 uuid.UUID `json:"id"`
	Name               string    `json:"name"`
	Username           string    `json:"username"`
	VerificationStatus string    `json:"verificationStatus"`
	LegalName          string    `json:"legalName,omitempty"`
}

// AdminTradeView is everything an operator needs to resolve a dispute.
type AdminTradeView struct {
	Trade   *models.P2PTrade   `json:"trade"`
	Side    string             `json:"side"`
	Buyer   P2PParty           `json:"buyer"`
	Seller  P2PParty           `json:"seller"`
	Payee   *models.P2PPayee   `json:"payee"`
	Dispute *models.P2PDispute `json:"dispute"`
	// EscrowAddress is the escrow on the trade's chain (checksummed): the
	// console refuses to sign a resolve built for any other contract.
	// Omitted when the chain has no escrow registered.
	EscrowAddress string `json:"escrowAddress,omitempty"`
}

// AdminTradeDetail loads a trade for operators: ungated payee (visible in any
// state, including after cancel) and the latest dispute with evidence.
func (s *P2PTradeService) AdminTradeDetail(ctx context.Context, tradeID uuid.UUID) (*AdminTradeView, error) {
	var t models.P2PTrade
	if err := s.db.WithContext(ctx).First(&t, "id = ?", tradeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTradeNotFound
		}
		return nil, err
	}
	// A pool slice is pool money: it is invisible outside the pool console.
	if err := hideSlice(&t); err != nil {
		return nil, err
	}
	fillTradeFee(&t)
	var side string
	if err := s.db.WithContext(ctx).Table("p2p_ads").Select("side").Where("id = ?", t.AdID).Scan(&side).Error; err != nil {
		return nil, err
	}
	var rows []struct {
		ID                 uuid.UUID
		Name               string
		Username           string
		VerificationStatus string
		VerifiedLegalName  string
	}
	if err := s.db.WithContext(ctx).Table("users").
		Select("id, name, username, verification_status, verified_legal_name").
		Where("id IN ?", []uuid.UUID{t.BuyerID, t.SellerID}).Scan(&rows).Error; err != nil {
		return nil, err
	}
	party := func(id uuid.UUID) P2PParty {
		for _, r := range rows {
			if r.ID == id {
				return P2PParty{ID: id, Name: r.Name, Username: r.Username,
					VerificationStatus: r.VerificationStatus, LegalName: r.VerifiedLegalName}
			}
		}
		return P2PParty{ID: id}
	}
	payee, err := s.loadPayee(ctx, &t)
	if err != nil {
		return nil, err
	}
	dispute, err := s.GetDispute(ctx, t.ID, uuid.Nil, true)
	if errors.Is(err, ErrDisputeNotFound) {
		dispute, err = nil, nil
	}
	if err != nil {
		return nil, err
	}
	view := &AdminTradeView{Trade: &t, Side: side, Buyer: party(t.BuyerID), Seller: party(t.SellerID), Payee: payee, Dispute: dispute}
	// The escrow the trade lives on, never the chain's current one: the console resolves the
	// dispute against this address. Empty for a trade opened before escrows were recorded.
	if common.IsHexAddress(t.EscrowAddress) {
		view.EscrowAddress = common.HexToAddress(t.EscrowAddress).Hex()
	}
	return view, nil
}
