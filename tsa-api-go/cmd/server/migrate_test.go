package main

import "testing"

func TestResolveMigrateMode(t *testing.T) {
	tests := []struct {
		name        string
		migrateFlag bool
		onBootEnv   string
		want        migrateMode
	}{
		{"default boot skips migration", false, "", migrateSkip},
		{"-migrate runs migration and exits", true, "", migrateOnly},
		{"MIGRATE_ON_BOOT=true restores legacy behaviour", false, "true", migrateOnBoot},
		{"MIGRATE_ON_BOOT=1 also accepted", false, "1", migrateOnBoot},
		{"MIGRATE_ON_BOOT=false is skip", false, "false", migrateSkip},
		{"unparseable MIGRATE_ON_BOOT is skip, not boot", false, "yes-please", migrateSkip},
		{"-migrate wins over MIGRATE_ON_BOOT", true, "true", migrateOnly},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveMigrateMode(tt.migrateFlag, tt.onBootEnv)
			if got != tt.want {
				t.Errorf("resolveMigrateMode(%v, %q) = %v, want %v",
					tt.migrateFlag, tt.onBootEnv, got, tt.want)
			}
		})
	}
}
