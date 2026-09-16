package main

import "strconv"

// migrateMode says how this process should treat schema migration.
//
// Migration moved out of boot because a deploy runs several processes against one
// database, and concurrent AutoMigrate calls race on DDL locks. The deploy runs
// migration exactly once, before any new process starts.
type migrateMode int

const (
	// migrateSkip is the normal boot path: the deploy already migrated.
	migrateSkip migrateMode = iota
	// migrateOnly runs migration and exits — the deploy's `-migrate` step.
	migrateOnly
	// migrateOnBoot restores pre-pipeline behaviour for local development, where
	// there is no deploy script to run the migrate step.
	migrateOnBoot
)

// resolveMigrateMode decides the mode. The -migrate flag always wins: it is an
// explicit operator instruction. MIGRATE_ON_BOOT is opt-in and fails closed —
// anything that does not parse as a true boolean means skip, so a typo cannot
// silently reintroduce concurrent boot migration.
func resolveMigrateMode(migrateFlag bool, onBootEnv string) migrateMode {
	if migrateFlag {
		return migrateOnly
	}
	if onBootEnv == "" {
		return migrateSkip
	}
	if b, err := strconv.ParseBool(onBootEnv); err == nil && b {
		return migrateOnBoot
	}
	return migrateSkip
}
