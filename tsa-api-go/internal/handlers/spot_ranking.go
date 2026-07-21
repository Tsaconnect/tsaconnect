package handlers

import (
	"sort"
	"strings"
)

// spot_ranking.go — ordering engine for the public spot list (PM ranking spec):
//   1. Admin PinnedPosition overrides win their 1-based slot
//      (conflict → lower display_order keeps it, the loser goes dynamic).
//   2. Default pins when unset: MCGP #1, BTC #2, ETH #3.
//   3. {BNB, USDT, SOL, XRP} fill the next slots (spec: 4–7) sorted by 24h
//      volume desc.
//   4. Everything else: market cap desc, volume desc, then display_order,
//      then symbol — so rows without market data keep the admin's manual order.

var spotDefaultPins = map[string]int{"MCGP": 1, "BTC": 2, "ETH": 3}

var spotVolumeGroup = map[string]bool{"BNB": true, "USDT": true, "SOL": true, "XRP": true}

func fOr(v *float64, missing float64) float64 {
	if v == nil {
		return missing
	}
	return *v
}

// stableLess is the shared deterministic tiebreak: admin display_order, then symbol.
func stableLess(a, b spotListingResponse) bool {
	if a.DisplayOrder != b.DisplayOrder {
		return a.DisplayOrder < b.DisplayOrder
	}
	return a.BaseSymbol < b.BaseSymbol
}

// rankSpotListings orders the enriched public list per the ranking spec above.
func rankSpotListings(rows []spotListingResponse) []spotListingResponse {
	if len(rows) <= 1 {
		return rows
	}
	ordered := make([]spotListingResponse, len(rows))
	copy(ordered, rows)
	sort.SliceStable(ordered, func(i, j int) bool { return stableLess(ordered[i], ordered[j]) })

	pinned := map[int]spotListingResponse{}
	taken := make([]bool, len(ordered))
	explicitlyPinnedSymbol := map[string]bool{}

	// Pass 1: explicit admin pins (ordered slice → conflict resolves to lower display_order).
	for i, r := range ordered {
		if r.PinnedPosition == nil || *r.PinnedPosition < 1 {
			continue
		}
		explicitlyPinnedSymbol[strings.ToUpper(r.BaseSymbol)] = true
		if _, occupied := pinned[*r.PinnedPosition]; occupied {
			continue // loser stays dynamic
		}
		pinned[*r.PinnedPosition] = r
		taken[i] = true
	}

	// Pass 2: default pins — first unpinned row per symbol, only when the admin
	// hasn't explicitly placed that symbol somewhere and the slot is free.
	for i, r := range ordered {
		if taken[i] {
			continue
		}
		sym := strings.ToUpper(r.BaseSymbol)
		slot, isDefault := spotDefaultPins[sym]
		if !isDefault || explicitlyPinnedSymbol[sym] {
			continue
		}
		if _, occupied := pinned[slot]; occupied {
			continue
		}
		pinned[slot] = r
		taken[i] = true
	}

	// Split the remainder into the fixed volume group and the dynamic rest.
	var volGroup, rest []spotListingResponse
	for i, r := range ordered {
		if taken[i] {
			continue
		}
		if spotVolumeGroup[strings.ToUpper(r.BaseSymbol)] {
			volGroup = append(volGroup, r)
		} else {
			rest = append(rest, r)
		}
	}
	sort.SliceStable(volGroup, func(i, j int) bool {
		vi, vj := fOr(volGroup[i].Volume24h, -1), fOr(volGroup[j].Volume24h, -1)
		if vi != vj {
			return vi > vj
		}
		return stableLess(volGroup[i], volGroup[j])
	})
	sort.SliceStable(rest, func(i, j int) bool {
		mi, mj := fOr(rest[i].MarketCap, -1), fOr(rest[j].MarketCap, -1)
		if mi != mj {
			return mi > mj
		}
		vi, vj := fOr(rest[i].Volume24h, -1), fOr(rest[j].Volume24h, -1)
		if vi != vj {
			return vi > vj
		}
		return stableLess(rest[i], rest[j])
	})
	dynamic := append(volGroup, rest...)

	// Merge: walk slots, placing pins at their positions and streaming the
	// dynamic pool into free slots; pins beyond the end append in slot order.
	out := make([]spotListingResponse, 0, len(ordered))
	next := 0
	for slot := 1; len(out) < len(ordered); slot++ {
		if r, ok := pinned[slot]; ok {
			out = append(out, r)
			delete(pinned, slot)
			continue
		}
		if next < len(dynamic) {
			out = append(out, dynamic[next])
			next++
			continue
		}
		// Only out-of-range pins remain — append them by ascending slot.
		slots := make([]int, 0, len(pinned))
		for s := range pinned {
			slots = append(slots, s)
		}
		sort.Ints(slots)
		for _, s := range slots {
			out = append(out, pinned[s])
		}
	}
	return out
}
