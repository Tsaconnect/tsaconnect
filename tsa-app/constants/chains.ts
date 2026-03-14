// constants/chains.ts
// Centralized chain & token registry for TSA Connect
// To add a new EVM chain: add an entry to CHAINS and update token `chains` arrays.

export interface ChainConfig {
  chainId: number;
  name: string;
  shortName: string;
  rpcUrl: string;
  explorerUrl: string;
  nativeCurrency: {
    symbol: string;
    decimals: number;
  };
  iconColor: string;
}

export interface TokenConfig {
  symbol: string;
  name: string;
  decimals: number;
  iconColor: string;
  chains: ChainKey[];
}

export type ChainKey = keyof typeof CHAINS;

export const CHAINS = {
  sonic: {
    chainId: 14601,
    name: 'Sonic Network',
    shortName: 'SONIC',
    rpcUrl: 'https://rpc.testnet.soniclabs.com',
    explorerUrl: 'https://testnet.sonicscan.org',
    nativeCurrency: { symbol: 'S', decimals: 18 },
    iconColor: '#5B6EF5',
  },
  bsc: {
    chainId: 97,
    name: 'BNB Smart Chain',
    shortName: 'BEP20',
    rpcUrl: 'https://data-seed-prebsc-1-s1.binance.org:8545',
    explorerUrl: 'https://testnet.bscscan.com',
    nativeCurrency: { symbol: 'tBNB', decimals: 18 },
    iconColor: '#F0B90B',
  },
} as const satisfies Record<string, ChainConfig>;

// Default tokens — used as fallback when backend is unreachable.
// The canonical token list is fetched from the backend at runtime.
export const DEFAULT_TOKENS: Record<string, TokenConfig> = {
  USDT: {
    symbol: 'USDT',
    name: 'Tether',
    decimals: 6,
    iconColor: '#26A17B',
    chains: ['sonic', 'bsc'],
  },
  USDC: {
    symbol: 'USDC',
    name: 'USD Coin',
    decimals: 6,
    iconColor: '#2775CA',
    chains: ['sonic', 'bsc'],
  },
  MCGP: {
    symbol: 'MCGP',
    name: 'MCG Protocol',
    decimals: 18,
    iconColor: '#FFD700',
    chains: ['sonic'],
  },
};

// Helpers (chain-only — these stay static)

export const CHAIN_KEYS = Object.keys(CHAINS) as ChainKey[];

export function getChain(key: ChainKey): ChainConfig {
  return CHAINS[key];
}

export function getChainByChainId(chainId: number): (ChainConfig & { key: ChainKey }) | undefined {
  const entry = Object.entries(CHAINS).find(([, c]) => c.chainId === chainId);
  if (!entry) return undefined;
  return { ...entry[1], key: entry[0] as ChainKey };
}

export function getSupportedNetworkNames(): string {
  return Object.values(CHAINS).map(c => c.name).join(' and ');
}

// Token helpers that accept a tokens map (used by useTokens hook)

export function getTokensForChain(tokens: Record<string, TokenConfig>, chainKey: ChainKey): TokenConfig[] {
  return Object.values(tokens).filter(t => t.chains.includes(chainKey));
}

export function getChainsForToken(tokens: Record<string, TokenConfig>, symbol: string): ChainConfig[] {
  const token = tokens[symbol];
  if (!token) return [];
  return token.chains.map(key => CHAINS[key]);
}
