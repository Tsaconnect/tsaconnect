package services

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Names on both sides are taken verbatim from the live lists (Paycrest
// /v1/institutions/NGN and a Nomba/Flutterwave NG bank list) so the matcher is
// exercised against the spelling variations that actually occur.

func TestPaycrestNetwork_Translation(t *testing.T) {
	cases := map[string]string{
		"base":     "base",
		"polygon":  "polygon",
		"bsc":      "bnb-smart-chain",
		"arbitrum": "arbitrum-one",
		"BSC":      "bnb-smart-chain", // case-insensitive
		" base ":   "base",            // trimmed
	}
	for ours, want := range cases {
		got, ok := PaycrestNetwork(ours)
		if !ok || got != want {
			t.Errorf("PaycrestNetwork(%q) = %q,%v want %q,true", ours, got, ok, want)
		}
	}
	// Sonic is our own chain and Paycrest does not serve it. Returning a
	// guessed name would surface as "no provider", which reads as a liquidity
	// problem instead of an unsupported chain.
	for _, unsupported := range []string{"sonic", "optimism", "avalanche", "linea", ""} {
		if got, ok := PaycrestNetwork(unsupported); ok {
			t.Errorf("PaycrestNetwork(%q) = %q, want not-supported", unsupported, got)
		}
	}
}

func TestChainKeyForPaycrestNetwork_RoundTrips(t *testing.T) {
	for _, ours := range PaycrestNetworkKeys() {
		theirs, ok := PaycrestNetwork(ours)
		if !ok {
			t.Fatalf("%s listed but not translatable", ours)
		}
		back, ok := ChainKeyForPaycrestNetwork(theirs)
		if !ok || back != ours {
			t.Errorf("round trip %s -> %s -> %s (ok=%v)", ours, theirs, back, ok)
		}
	}
	if _, ok := ChainKeyForPaycrestNetwork("solana"); ok {
		t.Error("solana should not resolve to one of our chain keys")
	}
}

func TestBuildPaycrestBankMap_MatchesRealWorldNameVariants(t *testing.T) {
	pc := []PaycrestInstitution{
		{Name: "Guaranty Trust Bank", Code: "GTBINGLA", Type: "bank"},
		{Name: "Access Bank", Code: "ABNGNGLA", Type: "bank"},
		{Name: "First Bank Of Nigeria", Code: "FBNINGLA", Type: "bank"},
		{Name: "OPay", Code: "OPAYNGPC", Type: "bank"},
		{Name: "United Bank for Africa", Code: "UNAFNGLA", Type: "bank"},
		{Name: "FCMB", Code: "FCMBNGLA", Type: "bank"},
		{Name: "Some Brand New MFB", Code: "NEWXNGPC", Type: "bank"},
	}
	ours := []NombaBank{
		{Name: "Guaranty Trust Bank Plc", Code: "058"},
		{Name: "Access Bank", Code: "044"},
		{Name: "First Bank of Nigeria Limited", Code: "011"},
		{Name: "OPay Digital Services Limited", Code: "999992"},
		{Name: "United Bank For Africa", Code: "033"},
		{Name: "First City Monument Bank", Code: "214"},
		{Name: "Stanbic IBTC Bank", Code: "221"},
	}

	m := buildPaycrestBankMap(pc, ours)

	want := map[string]string{
		"058":    "GTBINGLA",
		"044":    "ABNGNGLA",
		"011":    "FBNINGLA",
		"999992": "OPAYNGPC",
		"033":    "UNAFNGLA",
		"214":    "FCMBNGLA", // via override — the acronym shares no tokens
	}
	for nibss, pcCode := range want {
		if got := m.NIBSSToPaycrest[nibss]; got != pcCode {
			t.Errorf("NIBSS %s -> %q, want %q", nibss, got, pcCode)
		}
		if got := m.PaycrestToNIBSS[pcCode]; got != nibss {
			t.Errorf("Paycrest %s -> %q, want %q", pcCode, got, nibss)
		}
	}

	// A bank only Paycrest carries must be reported, never silently mapped to
	// whichever local name happened to score highest.
	if len(m.Unmatched) != 1 || m.Unmatched[0].Code != "NEWXNGPC" {
		t.Fatalf("unmatched = %#v, want exactly NEWXNGPC", m.Unmatched)
	}

	// A bank only we carry must not gain a Paycrest code.
	if _, ok := m.NIBSSToPaycrest["221"]; ok {
		t.Error("Stanbic IBTC has no Paycrest counterpart but was mapped anyway")
	}
}

// Routing money to the wrong bank is the worst possible failure here, so a
// weak match must lose to "unsupported".
func TestBuildPaycrestBankMap_RefusesWeakMatches(t *testing.T) {
	pc := []PaycrestInstitution{{Name: "Sparkle Microfinance Bank", Code: "SPRKNGPC"}}
	ours := []NombaBank{{Name: "Stanbic IBTC Bank", Code: "221"}}

	m := buildPaycrestBankMap(pc, ours)

	if len(m.NIBSSToPaycrest) != 0 {
		t.Fatalf("unrelated banks were matched: %#v", m.NIBSSToPaycrest)
	}
	if len(m.Unmatched) != 1 {
		t.Fatalf("want the Paycrest bank reported as unmatched, got %#v", m.Unmatched)
	}
}

// Two Paycrest entries scoring against one local bank must not overwrite each
// other — that would make the mapping depend on list order.
func TestBuildPaycrestBankMap_FirstMatchWinsPerNIBSSCode(t *testing.T) {
	pc := []PaycrestInstitution{
		{Name: "Access Bank", Code: "ABNGNGLA"},
		{Name: "Access Bank", Code: "DUPEXXXX"},
	}
	ours := []NombaBank{{Name: "Access Bank", Code: "044"}}

	m := buildPaycrestBankMap(pc, ours)

	if m.NIBSSToPaycrest["044"] != "ABNGNGLA" {
		t.Errorf("044 -> %q, want the first match ABNGNGLA", m.NIBSSToPaycrest["044"])
	}
	if len(m.Unmatched) != 1 || m.Unmatched[0].Code != "DUPEXXXX" {
		t.Errorf("duplicate should be reported unmatched, got %#v", m.Unmatched)
	}
}

func TestPaycrestBankMap_Supports(t *testing.T) {
	m := &PaycrestBankMap{NIBSSToPaycrest: map[string]string{"058": "GTBINGLA"}}
	if !m.Supports("058") || !m.Supports(" 058 ") {
		t.Error("058 should be supported")
	}
	if m.Supports("221") {
		t.Error("221 should not be supported")
	}
	var nilMap *PaycrestBankMap
	if nilMap.Supports("058") {
		t.Error("a nil map supports nothing")
	}
}

func TestNormalizeBankName_DropsNoiseTokens(t *testing.T) {
	got := normalizeBankName("Guaranty Trust Bank Plc, Limited")
	for _, want := range []string{"guaranty", "trust"} {
		if !got[want] {
			t.Errorf("expected token %q in %v", want, got)
		}
	}
	for _, noise := range []string{"bank", "plc", "limited"} {
		if got[noise] {
			t.Errorf("noise token %q survived normalisation", noise)
		}
	}
	// "nigeria" is deliberately kept: dropping it collapses "First Bank Of
	// Nigeria" to {first}, which is then contained in "First City Monument
	// Bank". See TestBuildPaycrestBankMap_DoesNotConfuseFirstBankWithFCMB.
	if !normalizeBankName("First Bank Of Nigeria")["nigeria"] {
		t.Error("nigeria must survive normalisation to keep First Bank distinguishable")
	}
}

// Regression: the two banks whose normalised names nearly collide. Getting
// this wrong sends a user's payout to a different bank entirely.
func TestBuildPaycrestBankMap_DoesNotConfuseFirstBankWithFCMB(t *testing.T) {
	pc := []PaycrestInstitution{
		{Name: "First Bank Of Nigeria", Code: "FBNINGLA"},
		{Name: "FCMB", Code: "FCMBNGLA"},
	}
	ours := []NombaBank{
		{Name: "First Bank of Nigeria Limited", Code: "011"},
		{Name: "First City Monument Bank", Code: "214"},
	}

	m := buildPaycrestBankMap(pc, ours)

	if m.NIBSSToPaycrest["011"] != "FBNINGLA" {
		t.Errorf("011 (First Bank) -> %q, want FBNINGLA", m.NIBSSToPaycrest["011"])
	}
	if m.NIBSSToPaycrest["214"] != "FCMBNGLA" {
		t.Errorf("214 (FCMB) -> %q, want FCMBNGLA", m.NIBSSToPaycrest["214"])
	}
}

// The containment fallback must stay unambiguous: a brand name inside two
// different legal names is not a match, it is a coin flip with someone's money.
func TestBuildPaycrestBankMap_RefusesAmbiguousContainment(t *testing.T) {
	// Both locals contain {kuda} and neither is an exact match, so Jaccard
	// leaves them tied at 0.33 and the containment fallback sees two
	// candidates. Note "Kuda" vs "Kuda Bank Plc" would NOT be ambiguous —
	// noise stripping makes those an exact match, which is right.
	pc := []PaycrestInstitution{{Name: "Kuda", Code: "KUDANGPC"}}
	ours := []NombaBank{
		{Name: "Kuda Digital Services", Code: "090267"},
		{Name: "Kuda Trust Savings", Code: "999"},
	}

	m := buildPaycrestBankMap(pc, ours)

	if len(m.NIBSSToPaycrest) != 0 {
		t.Fatalf("ambiguous containment was resolved anyway: %#v", m.NIBSSToPaycrest)
	}
	if len(m.Unmatched) != 1 {
		t.Fatalf("want the ambiguous bank reported unmatched, got %#v", m.Unmatched)
	}
}

// ─── Directory plumbing ────────────────────────────────────────────────────

type stubInstitutions struct {
	rows  []PaycrestInstitution
	calls int
	err   error
}

func (s *stubInstitutions) Institutions(context.Context, string) ([]PaycrestInstitution, error) {
	s.calls++
	return s.rows, s.err
}

type stubBankDir struct {
	rows  []NombaBank
	calls int
}

func (s *stubBankDir) ListBanks(context.Context) ([]NombaBank, error) {
	s.calls++
	return s.rows, nil
}
func (s *stubBankDir) Resolve(context.Context, string, string) (NombaLookupResult, error) {
	return NombaLookupResult{}, nil
}

func TestPaycrestDirectory_CachesTheMap(t *testing.T) {
	pc := &stubInstitutions{rows: []PaycrestInstitution{{Name: "Access Bank", Code: "ABNGNGLA"}}}
	local := &stubBankDir{rows: []NombaBank{{Name: "Access Bank", Code: "044"}}}
	d := NewPaycrestDirectory(pc, local)

	for i := 0; i < 3; i++ {
		m, err := d.BankMap(context.Background(), "NGN")
		if err != nil {
			t.Fatalf("BankMap: %v", err)
		}
		if m.NIBSSToPaycrest["044"] != "ABNGNGLA" {
			t.Fatalf("mapping lost on call %d", i)
		}
	}
	if pc.calls != 1 || local.calls != 1 {
		t.Errorf("expected one fetch of each list, got paycrest=%d local=%d", pc.calls, local.calls)
	}
}

func TestPaycrestDirectory_DormantWhenUnwired(t *testing.T) {
	if d := NewPaycrestDirectory(nil, &stubBankDir{}); d != nil {
		t.Error("no Paycrest client should yield a nil directory")
	}
	if d := NewPaycrestDirectory(&stubInstitutions{}, nil); d != nil {
		t.Error("no bank directory should yield a nil directory")
	}
	var d *PaycrestDirectory
	if _, err := d.BankMap(context.Background(), "NGN"); !errors.Is(err, ErrPaycrestDisabled) {
		t.Errorf("want ErrPaycrestDisabled from a nil directory, got %v", err)
	}
}

func TestPaycrestDirectory_PropagatesFetchFailure(t *testing.T) {
	pc := &stubInstitutions{err: errors.New("upstream down")}
	d := NewPaycrestDirectory(pc, &stubBankDir{})
	if _, err := d.BankMap(context.Background(), "NGN"); err == nil {
		t.Fatal("expected the fetch failure to surface, not an empty map")
	}
}

func TestPaycrestDirectory_NonNigerianCurrenciesPassCodesThrough(t *testing.T) {
	pc := &stubInstitutions{rows: []PaycrestInstitution{
		{Name: "Equity Bank Kenya", Code: "EQBLKENA", Type: "bank"},
		{Name: "KCB Bank Kenya", Code: "KCBLKENX", Type: "bank"},
	}}
	// The local bank list is Nigerian and must not be consulted for KES.
	local := &stubBankDir{rows: []NombaBank{{Name: "Access Bank", Code: "044"}}}
	d := NewPaycrestDirectory(pc, local)

	got, err := d.Institutions(context.Background(), "KES")
	if err != nil {
		t.Fatalf("Institutions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d options, want both Kenyan banks: %#v", len(got), got)
	}
	if got[0].Code != "EQBLKENA" {
		t.Errorf("code = %q, want Paycrest's own code passed through", got[0].Code)
	}
	if local.calls != 0 {
		t.Errorf("the Nigerian bank list was fetched %d times for KES; it is irrelevant there", local.calls)
	}
}

func TestPaycrestDirectory_NigeriaStillMapsToNIBSS(t *testing.T) {
	pc := &stubInstitutions{rows: []PaycrestInstitution{
		{Name: "Access Bank", Code: "ABNGNGLA", Type: "bank"},
	}}
	local := &stubBankDir{rows: []NombaBank{{Name: "Access Bank", Code: "044"}}}
	d := NewPaycrestDirectory(pc, local)

	got, err := d.Institutions(context.Background(), "NGN")
	if err != nil {
		t.Fatalf("Institutions: %v", err)
	}
	if len(got) != 1 || got[0].Code != "044" {
		t.Fatalf("NGN must return NIBSS codes the app already speaks, got %#v", got)
	}
}

// ─── Destination kind ──────────────────────────────────────────────────────

// Every UGX destination is mobile money, and KES and TZS mix a few in among
// their banks, so the app cannot infer from the currency what to ask for. It
// asks for an account number when the answer is an MTN phone number, and the
// payout fails AFTER the on-chain leg — hence the type travels with the option.
func TestPaycrestDirectory_Institutions_CarriesTheDestinationKind(t *testing.T) {
	pc := &stubInstitutions{rows: []PaycrestInstitution{
		{Name: "MTN Mobile Money", Code: "MOMOUGPC", Type: "mobile_money"},
		{Name: "Stanbic Bank", Code: "SBICUGKX", Type: "bank"},
	}}
	d := NewPaycrestDirectory(pc, &stubBankDir{})

	opts, err := d.Institutions(context.Background(), "UGX")
	if err != nil {
		t.Fatalf("Institutions: %v", err)
	}
	byCode := map[string]string{}
	for _, o := range opts {
		byCode[o.Code] = o.Type
	}
	if byCode["MOMOUGPC"] != PaycrestInstitutionMobileMoney {
		t.Errorf("MTN type = %q, want mobile_money", byCode["MOMOUGPC"])
	}
	if byCode["SBICUGKX"] != PaycrestInstitutionBank {
		t.Errorf("Stanbic type = %q, want bank", byCode["SBICUGKX"])
	}
}

// An unset or unrecognised type must read as a bank: that is the majority
// destination everywhere, and it is what the app rendered for every currency
// before the field existed. Defaulting the other way would ask a Kenyan bank
// customer for a phone number.
func TestPaycrestDirectory_Institutions_UnknownKindDefaultsToBank(t *testing.T) {
	pc := &stubInstitutions{rows: []PaycrestInstitution{
		{Name: "Some Bank", Code: "AAAAKENX"},
		{Name: "Odd One", Code: "BBBBKENX", Type: "wallet"},
	}}
	d := NewPaycrestDirectory(pc, &stubBankDir{})

	opts, err := d.Institutions(context.Background(), "KES")
	if err != nil {
		t.Fatalf("Institutions: %v", err)
	}
	for _, o := range opts {
		if o.Type != PaycrestInstitutionBank {
			t.Errorf("%s type = %q, want bank", o.Code, o.Type)
		}
	}
}

// NGN goes through the NIBSS matcher rather than Paycrest's list, so its
// options are built by different code and would otherwise ship with an empty
// type — a field the app is about to branch on.
func TestPaycrestDirectory_Institutions_NGNOptionsAreBanks(t *testing.T) {
	pc := &stubInstitutions{rows: []PaycrestInstitution{{Name: "Access Bank", Code: "ABNGNGLA"}}}
	local := &stubBankDir{rows: []NombaBank{{Name: "Access Bank", Code: "044"}}}
	d := NewPaycrestDirectory(pc, local)

	opts, err := d.Institutions(context.Background(), "NGN")
	if err != nil {
		t.Fatalf("Institutions: %v", err)
	}
	if len(opts) == 0 {
		t.Fatal("no NGN options")
	}
	for _, o := range opts {
		if o.Type != PaycrestInstitutionBank {
			t.Errorf("NGN option %s type = %q, want bank", o.Code, o.Type)
		}
	}
}

// NGN was cached from the start (through BankMap); every other currency called
// Paycrest on every request. That only became a live path when the East
// African ramps opened, and the endpoint it sits behind is called whenever a
// payout picker opens.
func TestPaycrestDirectory_Institutions_CachesNonNGN(t *testing.T) {
	pc := &stubInstitutions{rows: []PaycrestInstitution{
		{Name: "Equity Bank Kenya", Code: "EQBLKENA", Type: "bank"},
	}}
	d := NewPaycrestDirectory(pc, &stubBankDir{})

	for i := 0; i < 5; i++ {
		if _, err := d.Institutions(context.Background(), "KES"); err != nil {
			t.Fatalf("Institutions: %v", err)
		}
	}
	if pc.calls != 1 {
		t.Errorf("Institutions fetched %d times, want 1 — the list changes when a "+
			"provider onboards a bank, not per request", pc.calls)
	}

	// A different currency is a different list, not a cache hit.
	if _, err := d.Institutions(context.Background(), "UGX"); err != nil {
		t.Fatalf("Institutions(UGX): %v", err)
	}
	if pc.calls != 2 {
		t.Errorf("calls = %d, want 2 — each currency caches separately", pc.calls)
	}
}

// The currency becomes a URL path segment at the provider. PathEscape does not
// escape ".", so a traversal-shaped value would be appended verbatim.
func TestPaycrestDirectory_Institutions_RejectsANonCurrencyCode(t *testing.T) {
	pc := &stubInstitutions{rows: []PaycrestInstitution{{Name: "X", Code: "X"}}}
	d := NewPaycrestDirectory(pc, &stubBankDir{})

	// "" is deliberately absent: an empty currency means NGN by long-standing
	// contract (TestPaycrestHandler_Banks_EmptyCurrencyBehavesAsNGN), and that
	// path never reaches the provider URL builder.
	for _, bad := range []string{"..", "../../admin", "KESX", "K", "kes/../ngn", "NGN%2F.."} {
		if _, err := d.Institutions(context.Background(), bad); err == nil {
			t.Errorf("Institutions(%q) = nil error; a value that is not a currency code "+
				"must never reach the provider URL", bad)
		}
	}
	if pc.calls != 0 {
		t.Errorf("provider called %d times for invalid input, want 0", pc.calls)
	}
}

// A provider blip must not empty a picker the user has open.
func TestPaycrestDirectory_Institutions_ServesAStaleListOnFailure(t *testing.T) {
	pc := &stubInstitutions{rows: []PaycrestInstitution{
		{Name: "Equity Bank Kenya", Code: "EQBLKENA", Type: "bank"},
	}}
	d := NewPaycrestDirectory(pc, &stubBankDir{})

	if _, err := d.Institutions(context.Background(), "KES"); err != nil {
		t.Fatalf("warm: %v", err)
	}
	// Expire, then break the upstream.
	d.instMu.Lock()
	d.institutions["KES"] = institutionEntry{rows: d.institutions["KES"].rows,
		at: time.Now().Add(-2 * institutionTTL)}
	d.instMu.Unlock()
	pc.err = errors.New("paycrest unreachable")

	opts, err := d.Institutions(context.Background(), "KES")
	if err != nil {
		t.Fatalf("stale read: %v", err)
	}
	if len(opts) != 1 || opts[0].Code != "EQBLKENA" {
		t.Errorf("options = %+v, want the previously cached list", opts)
	}
}
