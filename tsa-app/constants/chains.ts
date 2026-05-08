// constants/chains.ts
// Centralized chain & token registry for TSA Connect
//
// To add a new EVM chain:
//   1. Add a key to ChainKey
//   2. Add entries in MAINNET_CHAINS and TESTNET_CHAINS (testnet may be empty
//      if we don't ship support for that chain's testnet)
//   3. Add the BE counterpart in tsa-api-go/internal/config/config.go so the
//      backend can resolve balances for the new chain

export interface ChainConfig {
  chainId: number;
  name: string;
  shortName: string;
  rpcUrl: string;
  /**
   * Backup RPC endpoints, tried in order if the primary fails on broadcast.
   * Useful when a specific endpoint is unreachable from a user's carrier or
   * region (we've seen this with Sonic's official RPC on some Nigerian
   * networks).
   */
  fallbackRpcUrls?: string[];
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
export type ChainKey =
  | 'sonic'
  | 'bsc'
  | 'ethereum'
  | 'polygon'
  | 'arbitrum'
  | 'base'
  | 'optimism'
  | 'avalanche'
  | 'linea';

export const MAINNET_CHAINS: Record<ChainKey, ChainConfig> = {
  sonic: {
    chainId: 146,
    name: 'Sonic Network',
    shortName: 'SONIC',
    rpcUrl: 'https://rpc.soniclabs.com',
    fallbackRpcUrls: ['https://sonic.drpc.org'],
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
    fallbackRpcUrls: ['https://bsc-dataseed1.defibit.io', 'https://bsc-dataseed1.ninicoin.io'],
    explorerUrl: 'https://bscscan.com',
    nativeCurrency: { symbol: 'BNB', decimals: 18 },
    iconColor: '#F0B90B',
    iconUrl: 'https://coin-images.coingecko.com/coins/images/825/small/bnb-icon2_2x.png',
  },
  ethereum: {
    chainId: 1,
    name: 'Ethereum',
    shortName: 'ETH',
    rpcUrl: 'https://eth.llamarpc.com',
    fallbackRpcUrls: ['https://cloudflare-eth.com', 'https://ethereum-rpc.publicnode.com'],
    explorerUrl: 'https://etherscan.io',
    nativeCurrency: { symbol: 'ETH', decimals: 18 },
    iconColor: '#627EEA',
    iconUrl: 'https://coin-images.coingecko.com/coins/images/279/small/ethereum.png',
  },
  polygon: {
    chainId: 137,
    name: 'Polygon',
    shortName: 'MATIC',
    rpcUrl: 'https://polygon-rpc.com',
    fallbackRpcUrls: ['https://polygon.llamarpc.com', 'https://polygon-bor-rpc.publicnode.com'],
    explorerUrl: 'https://polygonscan.com',
    nativeCurrency: { symbol: 'POL', decimals: 18 },
    iconColor: '#8247E5',
    iconUrl: 'https://coin-images.coingecko.com/coins/images/4713/small/polygon.png',
  },
  arbitrum: {
    chainId: 42161,
    name: 'Arbitrum One',
    shortName: 'ARB',
    rpcUrl: 'https://arb1.arbitrum.io/rpc',
    fallbackRpcUrls: ['https://arbitrum.llamarpc.com', 'https://arbitrum-one-rpc.publicnode.com'],
    explorerUrl: 'https://arbiscan.io',
    nativeCurrency: { symbol: 'ETH', decimals: 18 },
    iconColor: '#28A0F0',
    iconUrl: 'https://coin-images.coingecko.com/coins/images/16547/small/arb.jpg',
  },
  base: {
    chainId: 8453,
    name: 'Base',
    shortName: 'BASE',
    rpcUrl: 'https://mainnet.base.org',
    fallbackRpcUrls: ['https://base.llamarpc.com', 'https://base-rpc.publicnode.com'],
    explorerUrl: 'https://basescan.org',
    nativeCurrency: { symbol: 'ETH', decimals: 18 },
    iconColor: '#0052FF',
    iconUrl: 'https://coin-images.coingecko.com/asset_platforms/images/131/small/base-network.png',
  },
  optimism: {
    chainId: 10,
    name: 'Optimism',
    shortName: 'OP',
    rpcUrl: 'https://mainnet.optimism.io',
    fallbackRpcUrls: ['https://optimism.llamarpc.com', 'https://optimism-rpc.publicnode.com'],
    explorerUrl: 'https://optimistic.etherscan.io',
    nativeCurrency: { symbol: 'ETH', decimals: 18 },
    iconColor: '#FF0420',
    iconUrl: 'https://coin-images.coingecko.com/coins/images/25244/small/Optimism.png',
  },
  avalanche: {
    chainId: 43114,
    name: 'Avalanche C-Chain',
    shortName: 'AVAX',
    rpcUrl: 'https://api.avax.network/ext/bc/C/rpc',
    fallbackRpcUrls: ['https://avalanche-c-chain-rpc.publicnode.com'],
    explorerUrl: 'https://snowtrace.io',
    nativeCurrency: { symbol: 'AVAX', decimals: 18 },
    iconColor: '#E84142',
    iconUrl: 'https://coin-images.coingecko.com/coins/images/12559/small/Avalanche_Circle_RedWhite_Trans.png',
  },
  linea: {
    chainId: 59144,
    name: 'Linea',
    shortName: 'LINEA',
    rpcUrl: 'https://rpc.linea.build',
    fallbackRpcUrls: ['https://linea-rpc.publicnode.com'],
    explorerUrl: 'https://lineascan.build',
    nativeCurrency: { symbol: 'ETH', decimals: 18 },
    iconColor: '#121212',
    iconUrl: 'https://coin-images.coingecko.com/asset_platforms/images/135/small/linea.jpeg',
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
  ethereum: {
    chainId: 11155111,
    name: 'Sepolia',
    shortName: 'SEP',
    rpcUrl: 'https://ethereum-sepolia-rpc.publicnode.com',
    fallbackRpcUrls: ['https://rpc.sepolia.org'],
    explorerUrl: 'https://sepolia.etherscan.io',
    nativeCurrency: { symbol: 'ETH', decimals: 18 },
    iconColor: '#627EEA',
    iconUrl: 'https://coin-images.coingecko.com/coins/images/279/small/ethereum.png',
  },
  polygon: {
    chainId: 80002,
    name: 'Polygon Amoy',
    shortName: 'MATIC',
    rpcUrl: 'https://rpc-amoy.polygon.technology',
    fallbackRpcUrls: ['https://polygon-amoy-bor-rpc.publicnode.com'],
    explorerUrl: 'https://amoy.polygonscan.com',
    nativeCurrency: { symbol: 'POL', decimals: 18 },
    iconColor: '#8247E5',
    iconUrl: 'https://coin-images.coingecko.com/coins/images/4713/small/polygon.png',
  },
  arbitrum: {
    chainId: 421614,
    name: 'Arbitrum Sepolia',
    shortName: 'ARB',
    rpcUrl: 'https://sepolia-rollup.arbitrum.io/rpc',
    fallbackRpcUrls: ['https://arbitrum-sepolia-rpc.publicnode.com'],
    explorerUrl: 'https://sepolia.arbiscan.io',
    nativeCurrency: { symbol: 'ETH', decimals: 18 },
    iconColor: '#28A0F0',
    iconUrl: 'https://coin-images.coingecko.com/coins/images/16547/small/arb.jpg',
  },
  base: {
    chainId: 84532,
    name: 'Base Sepolia',
    shortName: 'BASE',
    rpcUrl: 'https://sepolia.base.org',
    fallbackRpcUrls: ['https://base-sepolia-rpc.publicnode.com'],
    explorerUrl: 'https://sepolia.basescan.org',
    nativeCurrency: { symbol: 'ETH', decimals: 18 },
    iconColor: '#0052FF',
    iconUrl: 'https://coin-images.coingecko.com/asset_platforms/images/131/small/base-network.png',
  },
  optimism: {
    chainId: 11155420,
    name: 'OP Sepolia',
    shortName: 'OP',
    rpcUrl: 'https://sepolia.optimism.io',
    fallbackRpcUrls: ['https://optimism-sepolia-rpc.publicnode.com'],
    explorerUrl: 'https://sepolia-optimism.etherscan.io',
    nativeCurrency: { symbol: 'ETH', decimals: 18 },
    iconColor: '#FF0420',
    iconUrl: 'https://coin-images.coingecko.com/coins/images/25244/small/Optimism.png',
  },
  avalanche: {
    chainId: 43113,
    name: 'Avalanche Fuji',
    shortName: 'AVAX',
    rpcUrl: 'https://api.avax-test.network/ext/bc/C/rpc',
    fallbackRpcUrls: ['https://avalanche-fuji-c-chain-rpc.publicnode.com'],
    explorerUrl: 'https://testnet.snowtrace.io',
    nativeCurrency: { symbol: 'AVAX', decimals: 18 },
    iconColor: '#E84142',
    iconUrl: 'https://coin-images.coingecko.com/coins/images/12559/small/Avalanche_Circle_RedWhite_Trans.png',
  },
  linea: {
    chainId: 59141,
    name: 'Linea Sepolia',
    shortName: 'LINEA',
    rpcUrl: 'https://rpc.sepolia.linea.build',
    fallbackRpcUrls: ['https://linea-sepolia-rpc.publicnode.com'],
    explorerUrl: 'https://sepolia.lineascan.build',
    nativeCurrency: { symbol: 'ETH', decimals: 18 },
    iconColor: '#121212',
    iconUrl: 'https://coin-images.coingecko.com/asset_platforms/images/135/small/linea.jpeg',
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
//
// USDT/USDC are listed as multi-chain because users routinely bridge
// stablecoins between L1/L2s. Native gas tokens (ETH on most L2s, AVAX,
// POL) are listed per-chain so the wallet shows the correct symbol on each
// network's row.
export const DEFAULT_TOKENS: Record<string, TokenConfig> = {
  MCGP: {
    symbol: 'MCGP',
    name: 'MCG Protocol',
    decimals: 18,
    iconColor: '#FFD700',
    chains: ['sonic'],
  },
  S: {
    symbol: 'S',
    name: 'Sonic',
    decimals: 18,
    iconColor: '#5B6EF5',
    iconUrl: 'https://coin-images.coingecko.com/coins/images/38108/small/200x200_Sonic_Logo.png',
    chains: ['sonic'],
  },
  ETH: {
    symbol: 'ETH',
    name: 'Ether',
    decimals: 18,
    iconColor: '#627EEA',
    iconUrl: 'https://coin-images.coingecko.com/coins/images/279/small/ethereum.png',
    // ETH is the native gas token on Ethereum L1 and on most major L2s.
    chains: ['ethereum', 'arbitrum', 'base', 'optimism', 'linea'],
  },
  BNB: {
    symbol: 'BNB',
    name: 'BNB',
    decimals: 18,
    iconColor: '#F0B90B',
    iconUrl: 'https://coin-images.coingecko.com/coins/images/825/small/bnb-icon2_2x.png',
    chains: ['bsc'],
  },
  POL: {
    symbol: 'POL',
    name: 'Polygon',
    decimals: 18,
    iconColor: '#8247E5',
    iconUrl: 'https://coin-images.coingecko.com/coins/images/4713/small/polygon.png',
    chains: ['polygon'],
  },
  AVAX: {
    symbol: 'AVAX',
    name: 'Avalanche',
    decimals: 18,
    iconColor: '#E84142',
    iconUrl: 'https://coin-images.coingecko.com/coins/images/12559/small/Avalanche_Circle_RedWhite_Trans.png',
    chains: ['avalanche'],
  },
  USDT: {
    symbol: 'USDT',
    name: 'Tether',
    decimals: 6,
    iconColor: '#26A17B',
    iconUrl: 'https://coin-images.coingecko.com/coins/images/325/small/Tether.png',
    chains: ['sonic', 'bsc', 'ethereum', 'polygon', 'arbitrum', 'base', 'optimism', 'avalanche', 'linea'],
  },
  USDC: {
    symbol: 'USDC',
    name: 'USD Coin',
    decimals: 6,
    iconColor: '#2775CA',
    iconUrl: 'https://coin-images.coingecko.com/coins/images/6319/small/usdc.png',
    chains: ['sonic', 'bsc', 'ethereum', 'polygon', 'arbitrum', 'base', 'optimism', 'avalanche', 'linea'],
  },
};

// Helpers

export const CHAIN_KEYS: ChainKey[] = [
  'sonic',
  'bsc',
  'ethereum',
  'polygon',
  'arbitrum',
  'base',
  'optimism',
  'avalanche',
  'linea',
];

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
