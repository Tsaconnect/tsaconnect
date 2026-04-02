// constants/chains.ts
// Centralized chain & token registry for TSA Connect
// To add a new EVM chain: add entries to both MAINNET_CHAINS and TESTNET_CHAINS.

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
  iconUrl?: string;
}

export interface TokenConfig {
  symbol: string;
  name: string;
  decimals: number;
  iconColor: string;
  iconUrl?: string;
  chains: ChainKey[];
}

export type NetworkType = 'mainnet' | 'testnet';
export type ChainKey = 'sonic' | 'bsc';

export const MAINNET_CHAINS: Record<ChainKey, ChainConfig> = {
  sonic: {
    chainId: 146,
    name: 'Sonic Network',
    shortName: 'SONIC',
    rpcUrl: 'https://rpc.soniclabs.com',
    explorerUrl: 'https://sonicscan.org',
    nativeCurrency: { symbol: 'S', decimals: 18 },
    iconColor: '#5B6EF5',
    iconUrl: 'https://coin-images.coingecko.com/coins/images/38108/small/200x200_Sonic_Logo.png',
  },
  bsc: {
    chainId: 56,
    name: 'BNB Smart Chain',
    shortName: 'BEP20',
    rpcUrl: 'https://bsc-dataseed.binance.org',
    explorerUrl: 'https://bscscan.com',
    nativeCurrency: { symbol: 'BNB', decimals: 18 },
    iconColor: '#F0B90B',
    iconUrl: 'https://coin-images.coingecko.com/coins/images/825/small/bnb-icon2_2x.png',
  },
};

export const TESTNET_CHAINS: Record<ChainKey, ChainConfig> = {
  sonic: {
    chainId: 14601,
    name: 'Sonic Testnet',
    shortName: 'SONIC',
    rpcUrl: 'https://rpc.testnet.soniclabs.com',
    explorerUrl: 'https://testnet.sonicscan.org',
    nativeCurrency: { symbol: 'S', decimals: 18 },
    iconColor: '#5B6EF5',
    iconUrl: 'https://coin-images.coingecko.com/coins/images/38108/small/200x200_Sonic_Logo.png',
  },
  bsc: {
    chainId: 97,
    name: 'BSC Testnet',
    shortName: 'BEP20',
    rpcUrl: 'https://data-seed-prebsc-1-s1.binance.org:8545',
    explorerUrl: 'https://testnet.bscscan.com',
    nativeCurrency: { symbol: 'tBNB', decimals: 18 },
    iconColor: '#F0B90B',
    iconUrl: 'https://coin-images.coingecko.com/coins/images/825/small/bnb-icon2_2x.png',
  },
};

// Active chain config — defaults to mainnet, updated by useNetwork hook
export let CHAINS: Record<ChainKey, ChainConfig> = { ...MAINNET_CHAINS };

export function setActiveNetwork(network: NetworkType) {
  if (network === 'testnet') {
    Object.assign(CHAINS, TESTNET_CHAINS);
  } else {
    Object.assign(CHAINS, MAINNET_CHAINS);
  }
}

// Default tokens — used as fallback when backend is unreachable.
// The canonical token list is fetched from the backend at runtime.
export const DEFAULT_TOKENS: Record<string, TokenConfig> = {
  USDT: {
    symbol: 'USDT',
    name: 'Tether',
    decimals: 6,
    iconColor: '#26A17B',
    iconUrl: 'https://coin-images.coingecko.com/coins/images/325/small/Tether.png',
    chains: ['sonic', 'bsc'],
  },
  USDC: {
    symbol: 'USDC',
    name: 'USD Coin',
    decimals: 6,
    iconColor: '#2775CA',
    iconUrl: 'https://coin-images.coingecko.com/coins/images/6319/small/usdc.png',
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

// Helpers

export const CHAIN_KEYS: ChainKey[] = ['sonic', 'bsc'];

export function getChain(key: ChainKey): ChainConfig {
  return CHAINS[key];
}

export function getChainsForNetwork(network: NetworkType): Record<ChainKey, ChainConfig> {
  return network === 'testnet' ? TESTNET_CHAINS : MAINNET_CHAINS;
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
