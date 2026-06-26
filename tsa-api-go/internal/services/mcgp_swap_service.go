package services

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

// MCGPSwapService is the backend adapter for McgpEntitlementSwap ("Swap
// Contract B"). It follows the signed-but-not-broadcast pattern used across the
// codebase: the backend PREPARES unsigned txs (the user signs + broadcasts in
// the app), and CONFIRMS by verifying the on-chain event. Reads are eth_call.
//
// Dormant-by-default: when MCGP_SWAP_B_ADDRESS is unset, IsConfigured() is false
// and the routes are not registered.
type MCGPSwapService struct {
	client  *blockchain.EVMClient
	address string
	cfg     *config.Config
}

// NewMCGPSwapService returns a service bound to cfg.MCGPSwapBAddress on the
// given Sonic client.
func NewMCGPSwapService(client *blockchain.EVMClient, cfg *config.Config) *MCGPSwapService {
	addr := ""
	if cfg != nil {
		addr = cfg.MCGPSwapBAddress
	}
	return &MCGPSwapService{client: client, address: addr, cfg: cfg}
}

// IsConfigured reports whether the swap contract address is set (gates routes).
func (s *MCGPSwapService) IsConfigured() bool {
	return s != nil && s.address != ""
}

// ─────────────────────────── unsigned tx builder ──────────────────────────
// buildUnsignedTx mirrors EscrowService.buildUnsignedTx: in non-production with
// no client it returns a minimal tx (to+data) for tests; otherwise it fills
// nonce/gas/chainId from the chain.
func (s *MCGPSwapService) buildUnsignedTx(from string, callData []byte) ([]byte, error) {
	if s.address == "" {
		return nil, fmt.Errorf("MCGP swap contract address is empty")
	}
	if s.client == nil {
		if s.cfg != nil && s.cfg.Env == "production" {
			return nil, fmt.Errorf("blockchain client not available")
		}
		tx := blockchain.UnsignedTx{To: s.address, Value: "0", Data: common.Bytes2Hex(callData)}
		return json.Marshal(tx)
	}

	fromAddr := common.HexToAddress(from)
	contractAddr := common.HexToAddress(s.address)

	nonce, err := s.client.GetNonce(from)
	if err != nil {
		return nil, fmt.Errorf("failed to get nonce: %w", err)
	}
	gasPrice, err := s.client.SuggestGasPrice()
	if err != nil {
		return nil, fmt.Errorf("failed to get gas price: %w", err)
	}
	gasLimit, err := s.client.EstimateGas(ethereum.CallMsg{From: fromAddr, To: &contractAddr, Data: callData})
	if err != nil {
		return nil, fmt.Errorf("gas estimation failed (transaction would likely revert): %w", err)
	}

	tx := blockchain.UnsignedTx{
		Nonce:    nonce,
		GasPrice: gasPrice.String(),
		GasLimit: gasLimit,
		To:       s.address,
		Value:    "0",
		Data:     common.Bytes2Hex(callData),
		ChainID:  s.client.ChainID().String(),
	}
	return json.Marshal(tx)
}

func (s *MCGPSwapService) pack(method string, args ...interface{}) ([]byte, error) {
	data, err := parsedMCGPSwapABI.Pack(method, args...)
	if err != nil {
		return nil, fmt.Errorf("pack %s: %w", method, err)
	}
	return data, nil
}

// ────────────────────────────── prepare (write) ───────────────────────────

// PrepareBuyPremium builds an unsigned buyPremium(mcgpAmount, maxUsdc) tx.
func (s *MCGPSwapService) PrepareBuyPremium(from string, mcgpAmount, maxUsdc *big.Int) ([]byte, error) {
	data, err := s.pack("buyPremium", mcgpAmount, maxUsdc)
	if err != nil {
		return nil, err
	}
	return s.buildUnsignedTx(from, data)
}

// PrepareSpend builds an unsigned spend(to, amount, ref) tx (marketplace order
// or peer pay). ref is a single-use idempotency key, e.g. keccak256(orderID).
func (s *MCGPSwapService) PrepareSpend(from, to string, amount *big.Int, ref [32]byte) ([]byte, error) {
	data, err := s.pack("spend", common.HexToAddress(to), amount, ref)
	if err != nil {
		return nil, err
	}
	return s.buildUnsignedTx(from, data)
}

// PrepareSellPremium builds an unsigned sellPremium(mcgpAmount, minUsdc) tx.
func (s *MCGPSwapService) PrepareSellPremium(from string, mcgpAmount, minUsdc *big.Int) ([]byte, error) {
	data, err := s.pack("sellPremium", mcgpAmount, minUsdc)
	if err != nil {
		return nil, err
	}
	return s.buildUnsignedTx(from, data)
}

// PrepareSellLegacy builds an unsigned sellLegacy(mcgpAmount, minUsdc) tx
// (redeem-only legacy off-ramp, capped at the snapshot entitlement).
func (s *MCGPSwapService) PrepareSellLegacy(from string, mcgpAmount, minUsdc *big.Int) ([]byte, error) {
	data, err := s.pack("sellLegacy", mcgpAmount, minUsdc)
	if err != nil {
		return nil, err
	}
	return s.buildUnsignedTx(from, data)
}

// PrepareWithdrawPremium builds an unsigned withdrawPremiumToWallet(mcgpAmount)
// tx (trustless exit; withdrawn MCGP becomes ordinary, non-spendable wallet MCGP).
func (s *MCGPSwapService) PrepareWithdrawPremium(from string, mcgpAmount *big.Int) ([]byte, error) {
	data, err := s.pack("withdrawPremiumToWallet", mcgpAmount)
	if err != nil {
		return nil, err
	}
	return s.buildUnsignedTx(from, data)
}

// ──────────────────────────── ERC-20 approve ──────────────────────────────
// buyPremium pulls USDC and sellLegacy pulls MCGP, so the app must approve the
// swap contract as spender first. tokenAddr resolves the symbol to its config
// address; the swap contract (s.address) is always the spender.

func (s *MCGPSwapService) tokenAddr(token string) (string, error) {
	if s.cfg == nil {
		return "", fmt.Errorf("config not available")
	}
	switch strings.ToLower(strings.TrimSpace(token)) {
	case "usdc":
		return s.cfg.USDCTokenAddress, nil
	case "mcgp":
		return s.cfg.MCGPTokenAddress, nil
	}
	return "", fmt.Errorf("unsupported token %q (want usdc|mcgp)", token)
}

// Allowance reads the wallet's current ERC-20 allowance to the swap contract.
func (s *MCGPSwapService) Allowance(token, owner string) (*big.Int, error) {
	if s.client == nil {
		return nil, fmt.Errorf("blockchain client not available")
	}
	ta, err := s.tokenAddr(token)
	if err != nil {
		return nil, err
	}
	return s.client.GetTokenAllowance(ta, owner, s.address)
}

// PrepareApprove builds an unsigned ERC-20 approve(swapContract, amount) tx for
// the given token (usdc for buy, mcgp for legacy-sell).
func (s *MCGPSwapService) PrepareApprove(from, token string, amount *big.Int) ([]byte, error) {
	if s.client == nil {
		return nil, fmt.Errorf("blockchain client not available")
	}
	ta, err := s.tokenAddr(token)
	if err != nil {
		return nil, err
	}
	return s.client.PrepareERC20Approve(ta, from, s.address, amount)
}

// ────────────────────────────── reads (eth_call) ──────────────────────────

func (s *MCGPSwapService) callUint(method string, args ...interface{}) (*big.Int, error) {
	if s.client == nil {
		return nil, fmt.Errorf("blockchain client not available")
	}
	data, err := s.pack(method, args...)
	if err != nil {
		return nil, err
	}
	addr := common.HexToAddress(s.address)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := s.client.Client.CallContract(ctx, ethereum.CallMsg{To: &addr, Data: data}, nil)
	if err != nil {
		return nil, fmt.Errorf("%s call failed: %w", method, err)
	}
	vals, err := parsedMCGPSwapABI.Unpack(method, out)
	if err != nil || len(vals) == 0 {
		return nil, fmt.Errorf("decode %s: %w", method, err)
	}
	v, ok := vals[0].(*big.Int)
	if !ok {
		return nil, fmt.Errorf("%s returned unexpected type %T", method, vals[0])
	}
	return v, nil
}

// PremiumBalance returns the spendable premium MCGP balance of user (18dp).
func (s *MCGPSwapService) PremiumBalance(user string) (*big.Int, error) {
	return s.callUint("premiumBalance", common.HexToAddress(user))
}

// LegacyEntitlement returns user's snapshot legacy entitlement cap (18dp).
func (s *MCGPSwapService) LegacyEntitlement(user string) (*big.Int, error) {
	return s.callUint("legacyEntitlement", common.HexToAddress(user))
}

// LegacyRedeemed returns the cumulative legacy MCGP user has redeemed (18dp).
func (s *MCGPSwapService) LegacyRedeemed(user string) (*big.Int, error) {
	return s.callUint("legacyRedeemed", common.HexToAddress(user))
}

// LegacyRemaining returns the legacy MCGP user may still redeem (18dp).
func (s *MCGPSwapService) LegacyRemaining(user string) (*big.Int, error) {
	return s.callUint("legacyRemaining", common.HexToAddress(user))
}

// Quote returns the USDC (6dp) for mcgpAmount (18dp) at the active phase of kind.
func (s *MCGPSwapService) Quote(kind uint8, mcgpAmount *big.Int) (*big.Int, error) {
	return s.callUint("quote", kind, mcgpAmount)
}

// Backing returns (held MCGP, owed = accountedMcgp). The money invariant the
// reconciler asserts is held >= owed.
func (s *MCGPSwapService) Backing() (held, owed *big.Int, err error) {
	if s.client == nil {
		return nil, nil, fmt.Errorf("blockchain client not available")
	}
	data, err := s.pack("backing")
	if err != nil {
		return nil, nil, err
	}
	addr := common.HexToAddress(s.address)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := s.client.Client.CallContract(ctx, ethereum.CallMsg{To: &addr, Data: data}, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("backing call failed: %w", err)
	}
	vals, err := parsedMCGPSwapABI.Unpack("backing", out)
	if err != nil || len(vals) < 2 {
		return nil, nil, fmt.Errorf("decode backing: %w", err)
	}
	held, _ = vals[0].(*big.Int)
	owed, _ = vals[1].(*big.Int)
	if held == nil || owed == nil {
		return nil, nil, fmt.Errorf("backing returned unexpected types")
	}
	return held, owed, nil
}

// ──────────────────────────── confirm (verify event) ──────────────────────

// PremiumSpentEvent is the decoded PremiumSpent log.
type PremiumSpentEvent struct {
	From   common.Address
	To     common.Address
	Amount *big.Int
	Ref    [32]byte
}

// VerifyPremiumSpent confirms a spend by locating the PremiumSpent log in the
// receipt that matches (from, to, ref) and returns it. Used to mark a
// marketplace order paid / a peer payment settled.
func (s *MCGPSwapService) VerifyPremiumSpent(txHash string, from, to common.Address, ref [32]byte) (*PremiumSpentEvent, error) {
	receipt, err := s.receipt(txHash)
	if err != nil {
		return nil, err
	}
	contractAddr := common.HexToAddress(s.address)
	sigID := parsedMCGPSwapABI.Events["PremiumSpent"].ID
	for _, lg := range receipt.Logs {
		if lg.Address != contractAddr || len(lg.Topics) < 4 || lg.Topics[0] != sigID {
			continue
		}
		evFrom := common.BytesToAddress(lg.Topics[1].Bytes())
		evTo := common.BytesToAddress(lg.Topics[2].Bytes())
		var evRef [32]byte
		copy(evRef[:], lg.Topics[3].Bytes())
		if evFrom != from || evTo != to || evRef != ref {
			continue
		}
		nonIndexed, err := parsedMCGPSwapABI.Events["PremiumSpent"].Inputs.NonIndexed().Unpack(lg.Data)
		if err != nil || len(nonIndexed) < 1 {
			return nil, fmt.Errorf("decode PremiumSpent data: %w", err)
		}
		return &PremiumSpentEvent{From: evFrom, To: evTo, Amount: nonIndexed[0].(*big.Int), Ref: evRef}, nil
	}
	return nil, fmt.Errorf("PremiumSpent(from=%s,to=%s) not found in receipt", from.Hex(), to.Hex())
}

// AmountEvent is the decoded form of the single-account swap events
// (PremiumBought / PremiumSold / PremiumWithdrawn / LegacyRedeemed).
type AmountEvent struct {
	Account    common.Address
	McgpAmount *big.Int
	UsdcAmount *big.Int // zero for PremiumWithdrawn (no USDC leg)
}

// VerifyAmountEvent confirms one of the single-account events by name, matching
// the indexed account, and returns the decoded amounts.
func (s *MCGPSwapService) VerifyAmountEvent(eventName, txHash string, account common.Address) (*AmountEvent, error) {
	ev, ok := parsedMCGPSwapABI.Events[eventName]
	if !ok {
		return nil, fmt.Errorf("unknown event %s", eventName)
	}
	receipt, err := s.receipt(txHash)
	if err != nil {
		return nil, err
	}
	contractAddr := common.HexToAddress(s.address)
	for _, lg := range receipt.Logs {
		if lg.Address != contractAddr || len(lg.Topics) < 2 || lg.Topics[0] != ev.ID {
			continue
		}
		if common.BytesToAddress(lg.Topics[1].Bytes()) != account {
			continue
		}
		nonIndexed, err := ev.Inputs.NonIndexed().Unpack(lg.Data)
		if err != nil || len(nonIndexed) < 1 {
			return nil, fmt.Errorf("decode %s data: %w", eventName, err)
		}
		out := &AmountEvent{Account: account, McgpAmount: nonIndexed[0].(*big.Int), UsdcAmount: big.NewInt(0)}
		if len(nonIndexed) >= 2 {
			if u, ok := nonIndexed[1].(*big.Int); ok {
				out.UsdcAmount = u
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("%s for %s not found in receipt", eventName, account.Hex())
}

func (s *MCGPSwapService) receipt(txHash string) (*types.Receipt, error) {
	if s.client == nil {
		return nil, fmt.Errorf("blockchain client not available")
	}
	// The app confirms right after broadcast, so the tx is usually not mined
	// yet on the node we query. Wait (Sonic blocks are ~1s) rather than failing
	// immediately on a single-shot lookup.
	r, err := s.client.WaitForReceipt(txHash, 60*time.Second)
	if err != nil {
		return nil, fmt.Errorf("get receipt %s: %w", txHash, err)
	}
	if r.Status != types.ReceiptStatusSuccessful {
		return nil, fmt.Errorf("tx %s reverted", txHash)
	}
	return r, nil
}
