package services

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/core/types"
)

const (
	// logScanMinChunk is the smallest span a logScanner halves down to before
	// it gives up and reports the RPC as unreadable.
	logScanMinChunk uint64 = 128
	// logScanRegrowAfter successful chunks in a row double a halved span again
	// (up to the configured maximum), so one transient refusal does not slow
	// every later scan for the life of the scanner.
	logScanRegrowAfter = 8
)

// logFilterer is the eth_getLogs slice of an Ethereum client;
// *ethclient.Client satisfies it.
type logFilterer interface {
	FilterLogs(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error)
}

// logScanner reads a block range of logs in chunks, halving the span whenever
// the RPC refuses one (range caps, result-count caps and timeouts all look
// alike from here) and doubling it again after a run of successes. It keeps
// that span between scans, so a long-lived owner learns its RPC's limits once.
type logScanner struct {
	chunk     uint64 // current eth_getLogs span; 0 = unbounded
	maxChunk  uint64 // configured span; regrowth stops here (0 = none)
	chunkRuns int    // successful chunks since the span last changed
}

func newLogScanner(maxChunk uint64) logScanner {
	return logScanner{chunk: maxChunk, maxChunk: maxChunk}
}

// scanLogs reads the logs q matches in [from, to] (q's own block range is
// ignored). apply runs once per successful chunk, with the chunk's last block;
// an error from it stops the scan and is returned as is. A chunk the RPC still
// refuses at the minimum span — or once ctx is done — ends the scan with the
// RPC's error, wrapped.
func (l *logScanner) scanLogs(ctx context.Context, src logFilterer, q ethereum.FilterQuery, from, to uint64,
	callTimeout time.Duration, apply func(logs []types.Log, chunkEnd uint64) error) error {
	for from <= to {
		end := to
		if l.chunk > 0 && to-from+1 > l.chunk {
			end = from + l.chunk - 1
		}
		q.FromBlock, q.ToBlock = new(big.Int).SetUint64(from), new(big.Int).SetUint64(end)
		callCtx, cancel := context.WithTimeout(ctx, callTimeout)
		logs, err := src.FilterLogs(callCtx, q)
		cancel()
		if err != nil {
			span := end - from + 1
			if ctx.Err() != nil || span <= logScanMinChunk {
				return fmt.Errorf("eth_getLogs %d-%d: %w", from, end, err)
			}
			l.chunk, l.chunkRuns = span/2, 0
			continue
		}
		if err := apply(logs, end); err != nil {
			return err
		}
		from = end + 1
		l.regrowChunk()
	}
	return nil
}

// regrowChunk doubles a halved span after logScanRegrowAfter full-size chunks
// in a row, never past the configured maximum.
func (l *logScanner) regrowChunk() {
	if l.chunk == 0 || (l.maxChunk != 0 && l.chunk >= l.maxChunk) {
		return
	}
	l.chunkRuns++
	if l.chunkRuns < logScanRegrowAfter {
		return
	}
	l.chunk, l.chunkRuns = l.chunk*2, 0
	if l.maxChunk != 0 && l.chunk > l.maxChunk {
		l.chunk = l.maxChunk
	}
}
