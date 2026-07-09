package chains

import "context"

type Curve string

const (
	CurveSecp256k1 Curve = "secp256k1"
	CurveEd25519   Curve = "ed25519"
)

// Balance is one native or token balance for an address.
type Balance struct {
	Symbol       string `json:"symbol"`
	Raw          string `json:"raw"`          // integer string in base units
	Decimals     int    `json:"decimals"`
	TokenAddress string `json:"tokenAddress,omitempty"`
	IsNative     bool   `json:"isNative"`
}

// TransferRequest describes a send the user wants to make.
type TransferRequest struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Amount    string `json:"amount"`    // integer string, base units
	Symbol    string `json:"symbol"`    // native symbol or token symbol
	TokenAddr string `json:"tokenAddr"` // empty for native sends
}

// FeeQuote is the result of the mandatory pre-send gas check.
type FeeQuote struct {
	GasSymbol       string `json:"gasSymbol"`       // e.g. "TRX"
	EstFeeRaw       string `json:"estFeeRaw"`       // integer string in gas base units
	GasDecimals     int    `json:"gasDecimals"`
	NeedsActivation bool   `json:"needsActivation"` // recipient not yet on-chain (Tron ~1 TRX)
	HasEnoughGas    bool   `json:"hasEnoughGas"`    // sender's native balance covers EstFeeRaw
	Note            string `json:"note,omitempty"`  // human hint, e.g. "You need ~1 TRX to send USDT"
}

// SigningPayload is one hash the app must sign.
type SigningPayload struct {
	Index   int    `json:"index"`   // input index (BTC); 0 for single-sig chains
	HashHex string `json:"hashHex"` // hex of the 32-byte digest to sign
	Curve   Curve  `json:"curve"`
}

// PreparedTx is the opaque unsigned tx plus the payloads to sign.
type PreparedTx struct {
	Family   string           `json:"family"`
	Raw      string           `json:"raw"`      // family-specific serialized unsigned tx (JSON/hex)
	Payloads []SigningPayload `json:"payloads"`
}

// Signature is one app-produced signature for a payload.
type Signature struct {
	Index  int    `json:"index"`
	SigHex string `json:"sigHex"`
}

type TxStatus struct {
	Found         bool   `json:"found"`
	Confirmed     bool   `json:"confirmed"`
	Success       bool   `json:"success"` // false = reverted/failed even if mined
	Confirmations int64  `json:"confirmations"`
	Note          string `json:"note,omitempty"`
}

type Activity struct {
	Hash      string `json:"hash"`
	Direction string `json:"direction"` // "in" | "out"
	AmountRaw string `json:"amountRaw"`
	Symbol    string `json:"symbol"`
	Counter   string `json:"counterparty"`
	Status    string `json:"status"`
	BlockTime int64  `json:"blockTime"`
}

// ChainClient is implemented once per non-EVM family.
type ChainClient interface {
	Family() string
	ValidateAddress(addr string) error
	GetBalances(ctx context.Context, addr string) ([]Balance, error)
	EstimateFee(ctx context.Context, req TransferRequest) (FeeQuote, error)
	PrepareTransfer(ctx context.Context, req TransferRequest) (PreparedTx, error)
	AssembleAndBroadcast(ctx context.Context, prepared PreparedTx, sigs []Signature) (txHash string, err error)
	GetTxStatus(ctx context.Context, txHash string) (TxStatus, error)
	History(ctx context.Context, addr string, cursor string) (items []Activity, next string, err error)
}
