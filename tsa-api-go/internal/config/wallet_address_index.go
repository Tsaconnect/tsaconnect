package config

import (
	"log"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

const (
	// walletAddressCIIndex makes users.wallet_address unique case-insensitively.
	// An EVM address in a different case is the same wallet, so the old
	// case-sensitive index let a victim's address be linked to a second account
	// just by changing its case.
	walletAddressCIIndex = "uq_users_wallet_address_ci"
	// walletAddressLegacyIndex is the case-sensitive index it replaces. It is
	// kept whenever the new one cannot be built, so exact duplicates stay
	// impossible in the meantime.
	walletAddressLegacyIndex = "idx_users_wallet_address"
)

// migrateWalletAddressUniqueness replaces the case-sensitive unique index on
// users.wallet_address with a case-insensitive one, first cleaning up what
// would stop it building.
//
// Once the index exists (and, on Postgres, is valid) this returns at once: it
// scans users only on the boots that still have work to do.
//
// It never fails the boot. Production may hold two accounts on one address in
// different case — exactly what the new index forbids:
//   - deleted accounts never hold a wallet, so theirs are cleared first;
//   - in a group where exactly one account has PROVED the address (a used
//     wallet_link_challenges row), the others are links made before proofs
//     existed and are cleared, with an AUDIT log line naming the user ids;
//   - any other group is a human call: it is logged as an ALERT with the user
//     ids, the case-sensitive index is kept, and nothing new is built. The next
//     migrate after they are resolved builds it. The application compares
//     addresses case-insensitively either way.
//
// Diagnostic SQL:
//
//	SELECT LOWER(wallet_address), COUNT(*) FROM users
//	WHERE wallet_address <> '' GROUP BY 1 HAVING COUNT(*) > 1;
//
// Locking: on Postgres the index is built CONCURRENTLY (writes to users are
// not blocked; a failed build's INVALID leftover is dropped and retried on the
// next migrate). Elsewhere it is a plain CREATE INDEX, which locks users
// against writes for the build — milliseconds at this table's size.
func migrateWalletAddressUniqueness(db *gorm.DB) {
	if walletAddressCIIndexReady(db) {
		return
	}

	releaseDeletedAccountWallets(db)
	normaliseWalletAddresses(db)

	groups, err := walletAddressCaseDuplicates(db)
	if err != nil {
		log.Printf("[Migrate] WARNING could not check users.wallet_address for case-variant duplicates: %v", err)
		keepLegacyWalletAddressIndex(db)
		return
	}
	unresolved := resolveProvenDuplicates(db, groups)
	if len(unresolved) < len(groups) {
		normaliseWalletAddresses(db) // the surviving owners' rows were skipped above
	}
	if len(unresolved) > 0 {
		log.Printf("[Migrate] ALERT wallet_address_case_duplicates=%d — %s not built; "+
			"one address is linked to more than one account and none (or several) proved it. "+
			"Resolve each group, then re-run migrate.",
			len(unresolved), walletAddressCIIndex)
		for i, g := range unresolved {
			log.Printf("[Migrate] ALERT wallet_address_case_duplicate group=%d user_ids=%s", i+1, strings.Join(g.userIDs, ","))
		}
		keepLegacyWalletAddressIndex(db)
		return
	}

	if err := buildWalletAddressCIIndex(db); err != nil {
		log.Printf("[Migrate] WARNING could not build %s: %v", walletAddressCIIndex, err)
		keepLegacyWalletAddressIndex(db)
		return
	}
	// The case-insensitive index is strictly stronger; the old one is redundant.
	if err := db.Exec(`DROP INDEX IF EXISTS ` + walletAddressLegacyIndex).Error; err != nil {
		log.Printf("[Migrate] WARNING could not drop %s: %v", walletAddressLegacyIndex, err)
	}
}

func isPostgres(db *gorm.DB) bool { return db.Dialector.Name() == "postgres" }

// walletAddressCIIndexReady: the index exists and — on Postgres, where a failed
// CONCURRENTLY build leaves an INVALID index behind — is usable.
func walletAddressCIIndexReady(db *gorm.DB) bool {
	if isPostgres(db) {
		var valid bool
		err := db.Raw(`SELECT COALESCE(bool_and(i.indisvalid), false)
			FROM pg_index i
			JOIN pg_class c ON c.oid = i.indexrelid
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE c.relname = ? AND n.nspname = current_schema()`, walletAddressCIIndex).Scan(&valid).Error
		return err == nil && valid
	}
	return db.Migrator().HasIndex("users", walletAddressCIIndex)
}

func buildWalletAddressCIIndex(db *gorm.DB) error {
	const on = ` ON users (LOWER(wallet_address)) WHERE wallet_address <> ''`
	if !isPostgres(db) {
		return db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS ` + walletAddressCIIndex + on).Error
	}
	// An INVALID leftover from an interrupted build would satisfy IF NOT EXISTS
	// and never enforce anything; drop it first. CONCURRENTLY cannot run inside
	// a transaction — AutoMigrate's Exec calls are not in one.
	if err := db.Exec(`DROP INDEX CONCURRENTLY IF EXISTS ` + walletAddressCIIndex).Error; err != nil {
		return err
	}
	return db.Exec(`CREATE UNIQUE INDEX CONCURRENTLY ` + walletAddressCIIndex + on).Error
}

func keepLegacyWalletAddressIndex(db *gorm.DB) {
	if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS ` + walletAddressLegacyIndex +
		` ON users (wallet_address) WHERE wallet_address <> ''`).Error; err != nil {
		log.Printf("[Migrate] WARNING could not keep %s: %v", walletAddressLegacyIndex, err)
	}
}

// releaseDeletedAccountWallets clears wallets still recorded on deleted
// accounts. Account deletion already does this; rows from before it did would
// otherwise block the index on a wallet nobody can use.
func releaseDeletedAccountWallets(db *gorm.DB) {
	res := db.Exec(`UPDATE users SET wallet_address = '' WHERE account_status = ? AND wallet_address <> ''`,
		models.AccountStatusDeleted)
	if res.Error != nil {
		log.Printf("[Migrate] WARNING could not release wallets on deleted accounts: %v", res.Error)
		return
	}
	if res.RowsAffected > 0 {
		log.Printf("[Migrate] released wallet_address on %d deleted account(s)", res.RowsAffected)
	}
}

// normaliseWalletAddresses rewrites each stored EVM address to its checksum
// form. A row is only rewritten when no other row holds the same address in
// any case — rewriting one of a duplicate pair could collide with the other
// under the case-sensitive index, and those pairs are resolved separately.
func normaliseWalletAddresses(db *gorm.DB) {
	type row struct {
		ID            string
		WalletAddress string
	}
	var rows []row
	if err := db.Raw(`SELECT CAST(id AS TEXT) AS id, wallet_address FROM users WHERE wallet_address <> ''`).
		Scan(&rows).Error; err != nil {
		log.Printf("[Migrate] WARNING could not read users.wallet_address for normalisation: %v", err)
		return
	}
	perAddress := make(map[string]int, len(rows))
	for _, r := range rows {
		perAddress[strings.ToLower(r.WalletAddress)]++
	}
	rewritten, skipped := 0, 0
	for _, r := range rows {
		if !common.IsHexAddress(r.WalletAddress) || !strings.HasPrefix(r.WalletAddress, "0x") {
			skipped++
			continue
		}
		canonical := common.HexToAddress(r.WalletAddress).Hex()
		if canonical == r.WalletAddress {
			continue
		}
		if perAddress[strings.ToLower(r.WalletAddress)] > 1 {
			skipped++
			continue
		}
		// Guarded on the old value so a concurrent change is never overwritten.
		res := db.Exec(`UPDATE users SET wallet_address = ? WHERE id = ? AND wallet_address = ?`,
			canonical, r.ID, r.WalletAddress)
		if res.Error != nil {
			log.Printf("[Migrate] WARNING could not normalise wallet_address for user %s: %v", r.ID, res.Error)
			continue
		}
		rewritten += int(res.RowsAffected)
	}
	if rewritten > 0 || skipped > 0 {
		log.Printf("[Migrate] users.wallet_address normalised to checksum form: %d rewritten, %d left as-is (duplicate or not an EVM address)",
			rewritten, skipped)
	}
}

type walletDuplicateGroup struct {
	address string // lower-case
	userIDs []string
}

// walletAddressCaseDuplicates returns every group of accounts holding the same
// address in any letter case.
func walletAddressCaseDuplicates(db *gorm.DB) ([]walletDuplicateGroup, error) {
	var addresses []string
	if err := db.Raw(`SELECT LOWER(wallet_address) FROM users WHERE wallet_address <> ''
		GROUP BY LOWER(wallet_address) HAVING COUNT(*) > 1`).Scan(&addresses).Error; err != nil {
		return nil, err
	}
	groups := make([]walletDuplicateGroup, 0, len(addresses))
	for _, addr := range addresses {
		var ids []string
		if err := db.Raw(`SELECT CAST(id AS TEXT) FROM users WHERE LOWER(wallet_address) = ? ORDER BY created_at, id`, addr).
			Scan(&ids).Error; err != nil {
			return nil, err
		}
		groups = append(groups, walletDuplicateGroup{address: addr, userIDs: ids})
	}
	return groups, nil
}

// resolveProvenDuplicates clears the unproven links in every group where
// exactly one account proved the address, and returns the groups it could
// not resolve.
func resolveProvenDuplicates(db *gorm.DB, groups []walletDuplicateGroup) []walletDuplicateGroup {
	var unresolved []walletDuplicateGroup
	for _, g := range groups {
		var proven []string
		if err := db.Raw(`SELECT DISTINCT CAST(user_id AS TEXT) FROM wallet_link_challenges
			WHERE address = ? AND used_at IS NOT NULL AND CAST(user_id AS TEXT) IN ?`, g.address, g.userIDs).
			Scan(&proven).Error; err != nil {
			log.Printf("[Migrate] WARNING could not read wallet proofs: %v", err)
			unresolved = append(unresolved, g)
			continue
		}
		if len(proven) != 1 {
			unresolved = append(unresolved, g)
			continue
		}
		var others []string
		for _, id := range g.userIDs {
			if id != proven[0] {
				others = append(others, id)
			}
		}
		if err := db.Exec(`UPDATE users SET wallet_address = '' WHERE CAST(id AS TEXT) IN ? AND LOWER(wallet_address) = ?`,
			others, g.address).Error; err != nil {
			log.Printf("[Migrate] WARNING could not clear unproven wallet links: %v", err)
			unresolved = append(unresolved, g)
			continue
		}
		log.Printf("[Migrate] AUDIT wallet_link_evicted=1 owner_user=%s evicted_users=%s reason=unproven_duplicate",
			proven[0], strings.Join(others, ","))
	}
	return unresolved
}
