package services

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

// paycrest_directory.go — translation between Paycrest's vocabulary and ours.
//
// Two independent mappings live here because Paycrest names everything
// differently from the rest of this codebase:
//
//	banks    Paycrest uses SWIFT-style codes ("GTBINGLA", "OPAYNGPC");
//	         BankDirectory and the app's ngBanks.ts use NIBSS numerics ("058").
//	networks Paycrest uses "bnb-smart-chain" / "arbitrum-one"; Config.Networks
//	         uses "bsc" / "arbitrum".
//
// The bank mapping is resolved at RUNTIME by matching names, not from a
// hardcoded 171-row table. Both sides add banks — Nigeria licenses new MFBs
// constantly — and a static table silently rots into "bank not supported" for
// a user whose bank both providers actually carry. Matching by name means a
// bank added on both sides starts working with no deploy.

// ─── Networks ──────────────────────────────────────────────────────────────

// paycrestNetworkNames maps OUR chain key (Config.Networks / blockchainService)
// to Paycrest's. Only chains that appear on both sides are listed; anything
// absent is simply not offerable through Paycrest.
var paycrestNetworkNames = map[string]string{
	"base":     "base",
	"polygon":  "polygon",
	"ethereum": "ethereum",
	"bsc":      "bnb-smart-chain",
	"arbitrum": "arbitrum-one",
	"celo":     "celo",
	"lisk":     "lisk",
}

// PaycrestNetwork translates our chain key to Paycrest's. The bool is false for
// a chain Paycrest does not serve (sonic, optimism, avalanche, linea today) —
// callers must not guess a name, because an unknown network is quoted as "no
// provider" and looks like a liquidity problem rather than a config one.
func PaycrestNetwork(chainKey string) (string, bool) {
	n, ok := paycrestNetworkNames[strings.ToLower(strings.TrimSpace(chainKey))]
	return n, ok
}

// ChainKeyForPaycrestNetwork is the reverse, for turning a webhook or order
// response back into something blockchainService understands.
func ChainKeyForPaycrestNetwork(network string) (string, bool) {
	network = strings.ToLower(strings.TrimSpace(network))
	for ours, theirs := range paycrestNetworkNames {
		if theirs == network {
			return ours, true
		}
	}
	return "", false
}

// PaycrestNetworkKeys lists our chain keys that Paycrest can serve, so a
// caller can intersect them with whatever the live order book offers.
func PaycrestNetworkKeys() []string {
	out := make([]string, 0, len(paycrestNetworkNames))
	for k := range paycrestNetworkNames {
		out = append(out, k)
	}
	return out
}

// ─── Banks ─────────────────────────────────────────────────────────────────

// paycrestBankOverrides pins pairs that name-matching gets wrong or cannot
// see. Keys are NIBSS codes, values are Paycrest institution codes.
//
// Keep this list SHORT and justified — every entry is a claim that two names
// referring to the same bank cannot be matched mechanically. Prefer improving
// normalisation over adding a row.
var paycrestBankOverrides = map[string]string{
	// "FCMB" vs "First City Monument Bank" share no tokens once "bank" is
	// dropped, so nothing links the acronym to the expansion.
	"214": "FCMBNGLA",
	// NIBSS still lists Diamond post-merger; Paycrest carries it separately.
	"063": "DBLNNGLA",
}

// PaycrestBankMap is a resolved two-way mapping between NIBSS and Paycrest
// bank codes for one fiat currency.
type PaycrestBankMap struct {
	// NIBSSToPaycrest is what an offramp needs: the user picked a bank in our
	// vocabulary, Paycrest needs its own code.
	NIBSSToPaycrest map[string]string
	// PaycrestToNIBSS turns an order or webhook back into our vocabulary.
	PaycrestToNIBSS map[string]string
	// Names carries the local bank name for each mapped NIBSS code, so callers
	// can render a picker without a second lookup — and so an operator can see
	// WHICH banks mapped rather than a list of opaque codes.
	Names map[string]string
	// Unmatched are Paycrest institutions we could not tie to a NIBSS code.
	// Surfaced rather than dropped: a user whose bank is here gets a clear
	// "not supported for this payout method" instead of a silent failure, and
	// a big name appearing here is a bug in the matcher, not a fact about
	// Paycrest's coverage.
	Unmatched  []PaycrestInstitution
	ResolvedAt time.Time
}

// Supports reports whether a NIBSS bank code can be used with Paycrest.
func (m *PaycrestBankMap) Supports(nibssCode string) bool {
	if m == nil {
		return false
	}
	_, ok := m.NIBSSToPaycrest[strings.TrimSpace(nibssCode)]
	return ok
}

// PaycrestInstitutionLister is the slice of PaycrestClient this needs.
type PaycrestInstitutionLister interface {
	Institutions(ctx context.Context, fiat string) ([]PaycrestInstitution, error)
}

// PaycrestDirectory resolves and caches the bank mapping. Safe for concurrent
// use.
type PaycrestDirectory struct {
	pc    PaycrestInstitutionLister
	banks BankDirectory

	mu     sync.Mutex
	cached map[string]*PaycrestBankMap // key: fiat
}

// paycrestBankMapTTL matches the Flutterwave bank cache: the list changes on
// the order of weeks, and a stale entry only costs one unnecessary rejection.
const paycrestBankMapTTL = 24 * time.Hour

// NewPaycrestDirectory wires the two sides. Returns nil if either is missing,
// so callers gate on `!= nil` the same way they do for BankDirectory.
func NewPaycrestDirectory(pc PaycrestInstitutionLister, banks BankDirectory) *PaycrestDirectory {
	if pc == nil || banks == nil {
		return nil
	}
	return &PaycrestDirectory{pc: pc, banks: banks, cached: map[string]*PaycrestBankMap{}}
}

// BankMap returns the mapping for a currency, resolving and caching on first
// use.
func (d *PaycrestDirectory) BankMap(ctx context.Context, fiat string) (*PaycrestBankMap, error) {
	if d == nil {
		return nil, ErrPaycrestDisabled
	}
	fiat = strings.ToUpper(strings.TrimSpace(fiat))
	if fiat == "" {
		fiat = "NGN"
	}

	d.mu.Lock()
	if m, ok := d.cached[fiat]; ok && time.Since(m.ResolvedAt) < paycrestBankMapTTL {
		d.mu.Unlock()
		return m, nil
	}
	d.mu.Unlock()

	// Resolve outside the lock — both calls are network I/O and there is no
	// harm in two callers racing to build the same map.
	pcBanks, err := d.pc.Institutions(ctx, fiat)
	if err != nil {
		return nil, fmt.Errorf("paycrest directory: %w", err)
	}
	ours, err := d.banks.ListBanks(ctx)
	if err != nil {
		return nil, fmt.Errorf("paycrest directory: local bank list: %w", err)
	}

	m := buildPaycrestBankMap(pcBanks, ours)

	d.mu.Lock()
	d.cached[fiat] = m
	d.mu.Unlock()
	return m, nil
}

// buildPaycrestBankMap does the matching. Split out so it is testable without
// any network.
func buildPaycrestBankMap(pcBanks []PaycrestInstitution, ours []NombaBank) *PaycrestBankMap {
	m := &PaycrestBankMap{
		NIBSSToPaycrest: make(map[string]string, len(pcBanks)),
		PaycrestToNIBSS: make(map[string]string, len(pcBanks)),
		Names:           make(map[string]string, len(pcBanks)),
		ResolvedAt:      time.Now(),
	}
	localName := make(map[string]string, len(ours))
	for _, b := range ours {
		localName[b.Code] = b.Name
	}

	// Overrides win outright, before any name matching.
	byPaycrestCode := make(map[string]PaycrestInstitution, len(pcBanks))
	for _, p := range pcBanks {
		byPaycrestCode[p.Code] = p
	}
	claimed := make(map[string]bool, len(pcBanks)) // paycrest codes already mapped
	for nibss, pcCode := range paycrestBankOverrides {
		if _, exists := byPaycrestCode[pcCode]; !exists {
			continue // override names an institution Paycrest no longer lists
		}
		m.NIBSSToPaycrest[nibss] = pcCode
		m.PaycrestToNIBSS[pcCode] = nibss
		m.Names[nibss] = localName[nibss]
		claimed[pcCode] = true
	}

	// Index our banks by normalised token set.
	type localBank struct {
		code   string
		tokens map[string]bool
	}
	locals := make([]localBank, 0, len(ours))
	for _, b := range ours {
		if strings.TrimSpace(b.Code) == "" {
			continue
		}
		locals = append(locals, localBank{code: b.Code, tokens: normalizeBankName(b.Name)})
	}

	for _, p := range pcBanks {
		if claimed[p.Code] {
			continue
		}
		pt := normalizeBankName(p.Name)
		if len(pt) == 0 {
			m.Unmatched = append(m.Unmatched, p)
			continue
		}
		bestCode, bestScore := "", 0.0
		// containment tracks the other real-world pattern: a short brand name
		// wholly inside a longer legal one ("OPay" vs "OPay Digital Services
		// Limited"). Jaccard scores that at 0.33 and would reject it.
		var contained []string
		for _, l := range locals {
			if s := jaccard(pt, l.tokens); s > bestScore {
				bestScore, bestCode = s, l.code
			}
			if subset(pt, l.tokens) || subset(l.tokens, pt) {
				contained = append(contained, l.code)
			}
		}
		// 0.6 keeps "Guaranty Trust Bank" ↔ "Guaranty Trust Bank Plc" while
		// rejecting "Sparkle MFB" ↔ "Stanbic IBTC". Below it we would rather
		// say "unsupported" than route money to the wrong institution.
		if bestScore < 0.6 {
			// Fall back to containment, but ONLY when exactly one candidate
			// qualifies. "Union Bank" is contained in both "Union Bank Plc" and
			// "Union Homes Savings"; an ambiguous containment is worse than no
			// match, because the cost of being wrong is money at another bank.
			if len(contained) == 1 {
				bestCode, bestScore = contained[0], 1
			} else {
				m.Unmatched = append(m.Unmatched, p)
				continue
			}
		}
		if bestCode == "" {
			m.Unmatched = append(m.Unmatched, p)
			continue
		}
		// First match wins per NIBSS code: two Paycrest entries scoring against
		// the same local bank means one of them is wrong, and silently
		// overwriting would make the mapping depend on list order.
		if _, taken := m.NIBSSToPaycrest[bestCode]; taken {
			m.Unmatched = append(m.Unmatched, p)
			continue
		}
		m.NIBSSToPaycrest[bestCode] = p.Code
		m.PaycrestToNIBSS[p.Code] = bestCode
		m.Names[bestCode] = localName[bestCode]
	}
	return m
}

// bankNoise are tokens that carry no identity — every Nigerian bank name is
// full of them, and leaving them in makes unrelated banks look similar.
//
// "nigeria" is deliberately NOT here. Dropping it reduces "First Bank Of
// Nigeria" to the single token {first}, which is then wholly contained in
// "First City Monument Bank" — a wrong match on a major bank.
var bankNoise = map[string]bool{
	"bank": true, "plc": true, "limited": true, "ltd": true,
	"of": true, "the": true, "microfinance": true, "mfb": true,
	"company": true, "payment": true, "service": true, "psb": true, "and": true,
}

// subset reports whether every token of a appears in b.
func subset(a, b map[string]bool) bool {
	if len(a) == 0 || len(a) > len(b) {
		return false
	}
	for t := range a {
		if !b[t] {
			return false
		}
	}
	return true
}

var bankNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// normalizeBankName reduces a bank name to its identifying tokens.
func normalizeBankName(s string) map[string]bool {
	out := map[string]bool{}
	for _, tok := range bankNonAlnum.Split(strings.ToLower(strings.TrimSpace(s)), -1) {
		if tok == "" || bankNoise[tok] {
			continue
		}
		out[tok] = true
	}
	return out
}

// jaccard is intersection over union of two token sets.
func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for t := range a {
		if b[t] {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}
