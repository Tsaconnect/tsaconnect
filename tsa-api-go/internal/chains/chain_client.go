package chains

import (
	"context"
	"fmt"
)

// InvalidRequestError marks a TransferRequest the caller can fix — a malformed
// memo, a bad address, an amount below dust — as opposed to an upstream RPC or
// indexer failure. Handlers surface its Reason to the client verbatim and keep
// everything else generic, so an actionable message is not lost behind "fee
// estimate failed" on a path that moves money.
type InvalidRequestError struct{ Reason string }

func (e *InvalidRequestError) Error() string { return e.Reason }

// Invalid builds an InvalidRequestError with a formatted reason.
func Invalid(format string, a ...any) error {
	return &InvalidRequestError{Reason: fmt.Sprintf(format, a...)}
}

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

	// MemoHex is an optional bridge memo (hex, no 0x) the send must carry
	// on-chain. Some Bitcoin bridges identify a deposit solely by this memo — a
	// deposit that drops it is an unrecoverable send to the bridge's address.
	// Families that cannot carry one MUST reject a non-empty value rather than
	// silently ignore it.
	MemoHex string `json:"memoHex,omitempty"`

	// PSBTHex is an optional pre-built Bitcoin transaction (BIP-174, hex) to
	// sign instead of building one. Aggregators whose UTXO routes construct the
	// whole spend — inputs, bridge deposit, tag and change — hand it back this
	// way, and the exact output set is what makes the deposit creditable, so it
	// is signed as given rather than reconstructed. It is NOT trusted: see
	// prepareFromPSBT for what has to hold before a digest is handed out.
	// Families other than Bitcoin MUST reject a non-empty value.
	PSBTHex string `json:"psbtHex,omitempty"`

	// TxBytesB64 is the same idea as PSBTHex for account-based chains that hand
	// back a whole pre-built transaction: Sui's BCS TransactionData, base64.
	// (Two fields rather than one because the encodings are conventional to
	// their chains — PSBT is passed as hex everywhere, Sui tx bytes as base64 —
	// and silently reinterpreting one as the other is a way to sign nonsense.)
	// It is NOT trusted: see the family's prepare path for what must hold first.
	// Families that cannot sign one MUST reject a non-empty value.
	TxBytesB64 string `json:"txBytesB64,omitempty"`
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
	Decimals  int    `json:"decimals"`
	Symbol    string `json:"symbol"`
	Counter   string `json:"counterparty"`
	Status    string `json:"status"`
	BlockTime int64  `json:"blockTime"` // seconds since epoch
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
