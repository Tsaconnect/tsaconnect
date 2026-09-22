// utils/tokenLogo.ts
//
// The one place a token's logo is decided. Logos live in three spots today —
// DEFAULT_TOKENS (EVM tokens), NONEVM_CHAINS (TRX/BTC/SOL/SUI native coins) and
// a bundled MCGP image — and screens that checked only one of them drew a
// letter where another screen drew the logo.
import { DEFAULT_TOKENS, NONEVM_CHAINS } from '../constants/chains';

const MCGP_LOGO = require('../assets/digital/mcgp.png');

export type TokenLogoSource = { uri: string } | number;

// Contracts whose registry logo we vouch for when a third party (LiFi) hands us
// a token by address, keyed `${chainId}:${lowercase address}`. MCGP is the one
// that matters: LiFi carries no logo for it. Same address on both Sonic
// networks (tsa-api-go config.go sonicPresets).
const KNOWN_CONTRACTS: Record<string, string> = {
  '146:0x517600323e5e2938207fa2e2e915b9d80e5b2b21': 'MCGP',
  '14601:0x517600323e5e2938207fa2e2e915b9d80e5b2b21': 'MCGP',
};

/**
 * Whether a token identified by contract is one we curate — i.e. it may borrow
 * the registry logo for its symbol. Anything else must bring its own logo.
 */
export function isKnownContract(
  chainId: number | undefined,
  address: string | undefined,
  symbol: string,
): boolean {
  if (chainId == null || !address) return false;
  return KNOWN_CONTRACTS[`${chainId}:${address.toLowerCase()}`] === symbol.trim().toUpperCase();
}

/**
 * The logo for a token, or null when there is none to show.
 *
 * `explicitUri` — a LiFi logoURI, a listing's logoUrl, an indexer thumbnail —
 * describes that exact token and wins, but only over https: these URLs come
 * from admins and third parties and load straight into an image view.
 *
 * Registry logos are keyed by SYMBOL, so they are only borrowed for tokens we
 * curate. A discovered or imported contract can call itself "USDT"; giving it
 * Tether's logo would vouch for it. Pass `curated: false` for those.
 *
 * `preferRegistry` — for a curated coin shown across chains (the asset picker),
 * our own icon beats whichever chain's third-party logo came back, so USDC looks
 * the same there as in the wallet.
 */
export function tokenLogoSource(
  symbol: string,
  { explicitUri, curated = true, preferRegistry = false }:
    { explicitUri?: string | null; curated?: boolean; preferRegistry?: boolean } = {},
): TokenLogoSource | null {
  const uri = explicitUri?.trim();
  const explicit = uri && /^https:\/\//i.test(uri) ? { uri } : null;
  if (!curated) return explicit;
  if (preferRegistry) return registryLogo(symbol) ?? explicit;
  return explicit ?? registryLogo(symbol);
}

function registryLogo(symbol: string): TokenLogoSource | null {
  const key = symbol.trim().toUpperCase();
  if (key === 'MCGP') return MCGP_LOGO;
  const registry = DEFAULT_TOKENS[key]?.iconUrl;
  if (registry) return { uri: registry };
  const native = Object.values(NONEVM_CHAINS).find((c) => c.nativeCurrency.symbol.toUpperCase() === key);
  return native?.iconUrl ? { uri: native.iconUrl } : null;
}
