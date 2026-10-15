// utils/walletAssets.ts
//
// The wallet's asset list is assembled from several independent sources (the
// EVM fan-out, each non-EVM chain, the MCGP entitlement) plus the rows added by
// custom-token import and auto-discovery. They finish at wildly different
// times, so they are held separately and combined here rather than accumulated
// into one array as they arrive.
//
// Pulled out of screens/wallet.tsx so the ordering and merge rules can be
// tested without mounting a 1,500-line screen — the two bugs this replaced
// (reading React state before it committed, and clearing the list on every
// focus) were both invisible to type-checking.

/** The independent origins of wallet rows, in the order they render. */
export type AssetSource = 'evm' | 'tron' | 'btc' | 'solana' | 'sui' | 'premium';

export const ASSET_SOURCES: readonly AssetSource[] = [
  'evm',
  'tron',
  'btc',
  'solana',
  'sui',
  'premium',
] as const;

/** null means "has not reported yet" — distinct from [], which means "holds none". */
export type SourceAssets<T> = Record<AssetSource, T[] | null>;

export const emptySourceAssets = <T>(): SourceAssets<T> => ({
  evm: null,
  tron: null,
  btc: null,
  solana: null,
  sui: null,
  premium: null,
});

/**
 * Flatten the per-source rows into render order, then append the rows
 * contributed by custom-token import and discovery.
 *
 * Extras go last and are never allowed to displace a source row: a source read
 * is authoritative for a token it reports, whereas an extra was computed
 * against an earlier snapshot and may be stale.
 */
export function combineSourceAssets<T>(
  sources: SourceAssets<T>,
  extras: T[] = [],
): T[] {
  const rows: T[] = [];
  for (const source of ASSET_SOURCES) {
    const found = sources[source];
    if (found) rows.push(...found);
  }
  return extras.length ? [...rows, ...extras] : rows;
}

/** True once every source has reported, whether with rows or with none. */
export function allSourcesReported<T>(sources: SourceAssets<T>): boolean {
  return ASSET_SOURCES.every((source) => sources[source] !== null);
}

/** What the wallet screen last showed, kept so the next open paints at once. */
export interface WalletSnapshot<T> {
  sources: SourceAssets<T>;
  extras: T[];
}

export function isWalletSnapshot<T>(value: unknown): value is WalletSnapshot<T> {
  const v = value as WalletSnapshot<T> | null;
  return (
    !!v &&
    Array.isArray(v.extras) &&
    !!v.sources &&
    ASSET_SOURCES.every((source) => v.sources[source] === null || Array.isArray(v.sources[source]))
  );
}
