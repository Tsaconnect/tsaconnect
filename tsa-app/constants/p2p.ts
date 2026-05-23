// P2P-specific constants. Server is authoritative; values here mirror the
// backend's expectations so we can show inline guidance and pre-flight
// validation without round-tripping.

// Maximum absolute offset (percent) a seller may set on a float-rate ad.
// Mirrors the backend default cap (P2P_FLOAT_OFFSET_MAX_BPS=1000). The
// server is the source of truth — keep these in sync if ops widens the cap.
export const MAX_FLOAT_OFFSET_PCT = 10;

// Conversion helpers between human percent and basis points (the wire format).
export const pctToBps = (pct: number): number => Math.round(pct * 100);
export const bpsToPct = (bps: number): number => bps / 100;

// Shared palette for P2P screens. Hoisted out of per-screen inline GOLD
// constants so a colour drift in one place doesn't desync the marketplace
// visual identity. Mirrors the legacy values exactly; switch to
// constants/theme.js tokens when the wider design system is unified.
export const P2P_GOLD = {
  primary: '#FFD700',
  dark: '#B8860B',
  light: '#FFF8DC',
  muted: '#F5DEB3',
  background: '#FAF9F6',
  error: '#DC3545',
  success: '#2E8B57',
} as const;
