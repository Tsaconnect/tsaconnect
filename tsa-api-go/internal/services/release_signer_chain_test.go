package services

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
)

// nonceChain is a broadcaster whose pending nonce advances on each accepted
// broadcast — the shape that makes an unlocked build-then-send collide.
type nonceChain struct {
	mu   sync.Mutex
	id   int64
	next uint64
	sent []uint64
}

func (n *nonceChain) ChainID() *big.Int { return big.NewInt(n.id) }
func (n *nonceChain) SendRawTransaction(raw []byte) (string, error) {
	tx := new(types.Transaction)
	if err := rlp.DecodeBytes(raw, tx); err != nil {
		return "", err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sent = append(n.sent, tx.Nonce())
	if tx.Nonce() == n.next {
		n.next++
	}
	return tx.Hash().Hex(), nil
}
func (n *nonceChain) pending() uint64 { n.mu.Lock(); defer n.mu.Unlock(); return n.next }

func unsignedOn(chain int64, nonce uint64) ([]byte, error) {
	return json.Marshal(blockchain.UnsignedTx{Nonce: nonce, GasPrice: "1", GasLimit: 21000,
		To: "0x000000000000000000000000000000000000dEaD", Value: "0", ChainID: big.NewInt(chain).String()})
}

// Spec §24.2 / S8 / review I2.
func TestChainReleaseSigner_SendSerialisesPrepareAndBroadcast(t *testing.T) {
	key, _ := crypto.GenerateKey()
	bsc := &nonceChain{id: 56}
	s := NewChainReleaseSigner(key, map[int64]ReleaseBroadcaster{56: bsc})
	build := func() ([]byte, error) {
		n := bsc.pending()                // the nonce read every Prepare* does
		time.Sleep(20 * time.Millisecond) // a gas estimate between read and broadcast
		return unsignedOn(56, n)
	}
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Send(context.Background(), 56, build); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(bsc.sent) != 3 || bsc.sent[0] == bsc.sent[1] || bsc.sent[1] == bsc.sent[2] || bsc.next != 3 {
		t.Fatalf("nonces sent = %v next = %d, want 3 distinct consecutive nonces", bsc.sent, bsc.next)
	}
}

func TestChainReleaseSigner_ChainsDoNotBlockEachOther(t *testing.T) {
	key, _ := crypto.GenerateKey()
	sonic, bsc := &nonceChain{id: 146}, &nonceChain{id: 56}
	s := NewChainReleaseSigner(key, map[int64]ReleaseBroadcaster{146: sonic, 56: bsc})
	started := make(chan struct{})
	go s.Send(context.Background(), 146, func() ([]byte, error) {
		close(started)
		time.Sleep(300 * time.Millisecond)
		return unsignedOn(146, 0)
	})
	<-started
	begin := time.Now()
	if _, err := s.Send(context.Background(), 56, func() ([]byte, error) { return unsignedOn(56, 0) }); err != nil {
		t.Fatalf("bsc send: %v", err)
	}
	if time.Since(begin) > 150*time.Millisecond {
		t.Fatalf("a BSC send waited %v behind a slow Sonic send", time.Since(begin))
	}
}

func TestChainReleaseSigner_RoutesByChainID(t *testing.T) {
	key, _ := crypto.GenerateKey()
	sonic, bsc := &nonceChain{id: 146}, &nonceChain{id: 56}
	s := NewChainReleaseSigner(key, map[int64]ReleaseBroadcaster{146: sonic, 56: bsc})
	u, _ := unsignedOn(56, 0)
	if _, err := s.SignAndBroadcast(context.Background(), u); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if len(bsc.sent) != 1 || len(sonic.sent) != 0 {
		t.Fatalf("bsc = %v sonic = %v, want the BSC tx on BSC only", bsc.sent, sonic.sent)
	}
	if s.Address() != crypto.PubkeyToAddress(key.PublicKey).Hex() {
		t.Fatal("address")
	}
}

func TestChainReleaseSigner_UnknownChainNotBroadcast(t *testing.T) {
	key, _ := crypto.GenerateKey()
	sonic := &nonceChain{id: 146}
	s := NewChainReleaseSigner(key, map[int64]ReleaseBroadcaster{146: sonic})
	built := false
	_, err := s.Send(context.Background(), 56, func() ([]byte, error) { built = true; return unsignedOn(56, 0) })
	if !errors.Is(err, ErrTxNotBroadcast) || built {
		t.Fatalf("err = %v built = %v, want ErrTxNotBroadcast and nothing built", err, built)
	}
	u, _ := unsignedOn(56, 0)
	if _, err := s.SignAndBroadcast(context.Background(), u); !errors.Is(err, ErrTxNotBroadcast) || len(sonic.sent) != 0 {
		t.Fatalf("err = %v sonic = %v", err, sonic.sent)
	}
}

func TestChainReleaseSigner_RefusesATxBuiltForAnotherChain(t *testing.T) {
	key, _ := crypto.GenerateKey()
	sonic := &nonceChain{id: 146}
	s := NewChainReleaseSigner(key, map[int64]ReleaseBroadcaster{146: sonic})
	_, err := s.Send(context.Background(), 146, func() ([]byte, error) { return unsignedOn(56, 0) })
	if !errors.Is(err, ErrTxNotBroadcast) || len(sonic.sent) != 0 {
		t.Fatalf("err = %v sent = %v", err, sonic.sent)
	}
}

// chainCountingSender proves AutoRelease builds inside Send, on the trade's chain.
type chainCountingSender struct {
	fakeReleaseSigner
	sendChains []int64
	direct     int
}

func (c *chainCountingSender) SignAndBroadcast(ctx context.Context, u []byte) (string, error) {
	c.direct++
	return c.fakeReleaseSigner.SignAndBroadcast(ctx, u)
}
func (c *chainCountingSender) Send(ctx context.Context, chainID int64, build func() ([]byte, error)) (string, error) {
	c.sendChains = append(c.sendChains, chainID)
	return c.fakeReleaseSigner.Send(ctx, chainID, build)
}

func TestAutoRelease_BuildsUnderTheSignersChainLock(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	trade := seedOpenedPMTrade(t, svc)
	if _, err := svc.SystemMarkPaid(context.Background(), trade.ID, "nomba:evt_lock"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	// Money-invariant: AmountToBuyer + FeeToSystem must equal TokenAmount.
	tokenAmt, _ := new(big.Int).SetString(trade.TokenAmount, 10)
	fee := big.NewInt(500)
	esc.releasedEvent = &P2PTradeReleasedEvent{
		TradeID:       HashID(trade.ID),
		Buyer:         common.HexToAddress("0x000000000000000000000000000000000000beef"),
		AmountToBuyer: new(big.Int).Sub(tokenAmt, fee),
		FeeToSystem:   fee,
	}
	s := &chainCountingSender{fakeReleaseSigner: fakeReleaseSigner{addr: "0x1111111111111111111111111111111111111111", hash: "0xrel"}}
	if _, err := svc.AutoRelease(context.Background(), trade.ID, s); err != nil {
		t.Fatalf("AutoRelease: %v", err)
	}
	if len(s.sendChains) != 1 || s.sendChains[0] != trade.ChainID || s.direct != 0 {
		t.Fatalf("send chains = %v direct = %d, want one Send on chain %d and no direct sign", s.sendChains, s.direct, trade.ChainID)
	}
}
