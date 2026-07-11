package solana

import (
	"context"
	"fmt"

	bin "github.com/gagliardetto/binary"
	solanago "github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/programs/token"
	solanarpc "github.com/gagliardetto/solana-go/rpc"
)

// RPC is a thin wrapper over the solana-go JSON-RPC client, exposing just the
// operations the ChainClient needs. All Solana reads/broadcasts funnel through
// here so the backend stays the single upstream gateway (free-tier gateway).
type RPC struct {
	cl *solanarpc.Client
}

func NewRPC(endpoint string) *RPC {
	return &RPC{cl: solanarpc.New(endpoint)}
}

// heldToken is one SPL token account owned by an address: its mint and raw amount.
type heldToken struct {
	Mint   solanago.PublicKey
	Amount uint64
}

// SolBalance returns the native lamport balance for an address.
func (r *RPC) SolBalance(ctx context.Context, owner solanago.PublicKey) (uint64, error) {
	out, err := r.cl.GetBalance(ctx, owner, solanarpc.CommitmentConfirmed)
	if err != nil {
		return 0, err
	}
	return out.Value, nil
}

// HeldTokens returns every SPL token account the owner holds under the standard
// Token program, borsh-decoded to (mint, amount). Empty accounts are included so
// a zero balance still surfaces the token — the caller filters if it wants.
func (r *RPC) HeldTokens(ctx context.Context, owner solanago.PublicKey) ([]heldToken, error) {
	res, err := r.cl.GetTokenAccountsByOwner(ctx, owner,
		&solanarpc.GetTokenAccountsConfig{ProgramId: solanago.TokenProgramID.ToPointer()},
		&solanarpc.GetTokenAccountsOpts{Encoding: solanago.EncodingBase64, Commitment: solanarpc.CommitmentConfirmed},
	)
	if err != nil {
		return nil, err
	}
	out := make([]heldToken, 0, len(res.Value))
	for _, acc := range res.Value {
		var ta token.Account
		if err := bin.NewBorshDecoder(acc.Account.Data.GetBinary()).Decode(&ta); err != nil {
			continue // skip anything that isn't a standard token account
		}
		out = append(out, heldToken{Mint: ta.Mint, Amount: ta.Amount})
	}
	return out, nil
}

// MintDecimals batch-reads the decimals for a set of mints in a single
// getMultipleAccounts call, borsh-decoding each mint account. Missing/invalid
// mints are simply absent from the returned map.
func (r *RPC) MintDecimals(ctx context.Context, mints []solanago.PublicKey) (map[string]uint8, error) {
	out := map[string]uint8{}
	if len(mints) == 0 {
		return out, nil
	}
	res, err := r.cl.GetMultipleAccounts(ctx, mints...)
	if err != nil {
		return nil, err
	}
	for i, acc := range res.Value {
		if acc == nil {
			continue
		}
		var m token.Mint
		if err := bin.NewBorshDecoder(acc.Data.GetBinary()).Decode(&m); err != nil {
			continue
		}
		out[mints[i].String()] = m.Decimals
	}
	return out, nil
}

// MintInfo returns the decimals for a single mint and whether it decoded as a
// real SPL mint — used by the manual token-import path.
func (r *RPC) MintInfo(ctx context.Context, mint solanago.PublicKey) (decimals uint8, ok bool, err error) {
	acc, err := r.cl.GetAccountInfo(ctx, mint)
	if err != nil {
		return 0, false, err
	}
	if acc == nil || acc.Value == nil {
		return 0, false, nil
	}
	var m token.Mint
	if err := bin.NewBorshDecoder(acc.Value.Data.GetBinary()).Decode(&m); err != nil {
		return 0, false, nil
	}
	return m.Decimals, true, nil
}

// AccountExists reports whether an account is present on-chain — used to decide
// whether an SPL send must pay to create the recipient's associated token account.
func (r *RPC) AccountExists(ctx context.Context, addr solanago.PublicKey) (bool, error) {
	acc, err := r.cl.GetAccountInfo(ctx, addr)
	if err != nil {
		// solana-go returns a "not found" error for a missing account.
		if err == solanarpc.ErrNotFound {
			return false, nil
		}
		return false, err
	}
	return acc != nil && acc.Value != nil, nil
}

// LatestBlockhash fetches a recent blockhash for building a transaction.
func (r *RPC) LatestBlockhash(ctx context.Context) (solanago.Hash, error) {
	out, err := r.cl.GetLatestBlockhash(ctx, solanarpc.CommitmentConfirmed)
	if err != nil {
		return solanago.Hash{}, err
	}
	return out.Value.Blockhash, nil
}

// SendTx broadcasts a fully-signed transaction and returns its signature (txid).
func (r *RPC) SendTx(ctx context.Context, tx *solanago.Transaction) (string, error) {
	sig, err := r.cl.SendTransactionWithOpts(ctx, tx, solanarpc.TransactionOpts{
		SkipPreflight:       false,
		PreflightCommitment: solanarpc.CommitmentConfirmed,
	})
	if err != nil {
		return "", fmt.Errorf("send transaction: %w", err)
	}
	return sig.String(), nil
}

// SignatureStatus returns the confirmation status of a broadcast signature.
func (r *RPC) SignatureStatus(ctx context.Context, sig solanago.Signature) (*solanarpc.SignatureStatusesResult, error) {
	res, err := r.cl.GetSignatureStatuses(ctx, true, sig)
	if err != nil {
		return nil, err
	}
	if res == nil || len(res.Value) == 0 {
		return nil, nil
	}
	return res.Value[0], nil
}

// Signatures returns up to limit recent transaction signatures for an address.
func (r *RPC) Signatures(ctx context.Context, addr solanago.PublicKey, limit int) ([]*solanarpc.TransactionSignature, error) {
	l := limit
	return r.cl.GetSignaturesForAddressWithOpts(ctx, addr, &solanarpc.GetSignaturesForAddressOpts{
		Limit:      &l,
		Commitment: solanarpc.CommitmentConfirmed,
	})
}

// Transaction fetches a confirmed transaction (base64) for history parsing.
func (r *RPC) Transaction(ctx context.Context, sig solanago.Signature) (*solanarpc.GetTransactionResult, error) {
	maxVer := uint64(0)
	return r.cl.GetTransaction(ctx, sig, &solanarpc.GetTransactionOpts{
		Encoding:                       solanago.EncodingBase64,
		Commitment:                     solanarpc.CommitmentConfirmed,
		MaxSupportedTransactionVersion: &maxVer,
	})
}
