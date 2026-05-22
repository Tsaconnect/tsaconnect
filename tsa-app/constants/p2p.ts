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
