// What a payment link says once it leaves the screen, on the printed poster and
// on the QR card shared as a picture, so the two never drift apart. Fixed
// colours, not theme tokens: paper and pictures are light whatever the theme.
import type { CSSProperties } from 'react';

export const PRINT_COLORS = {
  ink: '#16201F',   /* 16.6:1 on white */
  muted: '#3D4A49', /* 9.2:1 on white */
  test: '#6B4E0E',  /* 7.7:1 on white */
} as const;

/** The same colours as CSS variables, for markup: `text-[color:var(--print-muted)]`. */
export const printColorVars = {
  '--print-ink': PRINT_COLORS.ink,
  '--print-muted': PRINT_COLORS.muted,
  '--print-test': PRINT_COLORS.test,
} as CSSProperties;

export const SCAN_TO_PAY = 'Scan to pay with TSA Connect';

/** "Test poster — no real money moves" */
export const testNotice = (what: 'poster' | 'link') => `Test ${what} — no real money moves`;
