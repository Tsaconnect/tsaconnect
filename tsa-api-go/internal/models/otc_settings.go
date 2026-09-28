package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// OTC listing categories — where the listing surfaces on the app's OTC tab.
const (
	OTCCategoryDirect  = "direct"
	OTCCategoryPresale = "presale"
)

// ValidOTCCategory reports whether c is a supported listing category.
func ValidOTCCategory(c string) bool {
	return c == OTCCategoryDirect || c == OTCCategoryPresale
}

// OTCSettings is the admin-editable control row for the OtcMarketplace desk
// behind the app's Easyswap "OTC" tab. It is a SINGLETON: the read path
// get-or-creates the one row and the admin PUT updates it in place.
//
//   - BuyEnabled / SellEnabled are app-side kill-switches per direction. Buy
//     gates the UI only; Sell is also enforced by POST /api/swap/prepare,
//     which refuses to build a sell while it is false. Neither stops a
//     transaction built elsewhere — the contract's own pause() is the
//     on-chain stop.
//   - LogoURL / AssetClass / Category are listing metadata (e.g. "RWA",
//     Direct vs Presale placement).
//
// The `default:true` tags only shape the DB column; writers must never Create
// with a false flag via struct fields (GORM omits zero values and the DB
// default would flip it back) — the seed row is all-defaults and every edit
// goes through Updates(map), so false always persists explicitly.
type OTCSettings struct {
	ID          uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	BuyEnabled  bool      `gorm:"not null;default:true" json:"buyEnabled"`
	SellEnabled bool      `gorm:"not null;default:true" json:"sellEnabled"`
	LogoURL     string    `json:"logoUrl"`
	AssetClass  string    `json:"assetClass"`
	Category    string    `gorm:"not null;default:'direct'" json:"category"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// TableName overrides the default table name.
func (OTCSettings) TableName() string {
	return "otc_settings"
}

// BeforeCreate assigns a UUID when one isn't set, so inserts work regardless of
// the DB's default (Postgres gen_random_uuid() vs SQLite in tests / seeds).
func (s *OTCSettings) BeforeCreate(tx *gorm.DB) error {
	if s.ID == uuid.Nil {
		s.ID = uuid.New()
	}
	return nil
}
