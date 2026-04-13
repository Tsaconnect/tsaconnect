import React, { createContext, useContext, useState, useEffect, useCallback, type ReactNode } from 'react';
import AsyncStorage from '@react-native-async-storage/async-storage';
import {
  DEFAULT_TOKENS,
  CHAINS,
  getTokensForChain,
  type TokenConfig,
  type ChainKey,
  type ChainConfig,
} from '../constants/chains';
import { fetchSupportedTokens } from '../services/walletApi';
import { getNetwork, useNetwork } from './useNetwork';

const CUSTOM_TOKENS_KEY_PREFIX = 'tsa-custom-tokens-';

export interface CustomToken extends TokenConfig {
  contractAddress: string;
  chainKey: ChainKey;
  custom: true;
  importedAt: string;
}

function getCustomTokensKey(network: string): string {
  return `${CUSTOM_TOKENS_KEY_PREFIX}${network}`;
}

async function loadCustomTokens(network: string): Promise<CustomToken[]> {
  try {
    const raw = await AsyncStorage.getItem(getCustomTokensKey(network));
    return raw ? JSON.parse(raw) : [];
  } catch { return []; }
}

async function saveCustomTokens(network: string, tokens: CustomToken[]): Promise<void> {
  await AsyncStorage.setItem(getCustomTokensKey(network), JSON.stringify(tokens));
}

const CACHE_KEY = 'tsa-supported-tokens';

export type ChainWithKey = ChainConfig & { key: ChainKey };

interface TokenContextValue {
  tokens: Record<string, TokenConfig>;
  tokenList: TokenConfig[];
  customTokens: CustomToken[];
  loading: boolean;
  refresh: () => Promise<void>;
  getTokensForChain: (chainKey: ChainKey) => TokenConfig[];
  getChainsForToken: (symbol: string) => ChainWithKey[];
  importToken: (token: CustomToken) => Promise<void>;
  removeToken: (symbol: string, chainKey: ChainKey) => Promise<void>;
}

const TokenContext = createContext<TokenContextValue | null>(null);

export function TokenProvider({ children }: { children: ReactNode }) {
  const [tokens, setTokens] = useState<Record<string, TokenConfig>>(DEFAULT_TOKENS);
  const [loading, setLoading] = useState(false);
  const [customTokens, setCustomTokens] = useState<CustomToken[]>([]);
  const { network } = useNetwork();

  // Load custom tokens on mount and when network changes
  useEffect(() => {
    loadCustomTokens(network).then(setCustomTokens);
  }, [network]);

  const importToken = useCallback(async (token: CustomToken) => {
    const network = getNetwork();
    const existing = await loadCustomTokens(network);
    if (existing.some(t => t.symbol === token.symbol && t.chainKey === token.chainKey)) {
      throw new Error(`${token.symbol} on ${token.chainKey} is already imported`);
    }
    const updated = [...existing, token];
    await saveCustomTokens(network, updated);
    setCustomTokens(updated);
  }, []);

  const removeToken = useCallback(async (symbol: string, chainKey: ChainKey) => {
    const network = getNetwork();
    const existing = await loadCustomTokens(network);
    const updated = existing.filter(t => !(t.symbol === symbol && t.chainKey === chainKey));
    await saveCustomTokens(network, updated);
    setCustomTokens(updated);
  }, []);

  const loadTokens = useCallback(async () => {
    setLoading(true);
    try {
      // Try cache first for instant UI
      const cached = await AsyncStorage.getItem(CACHE_KEY);
      if (cached) {
        const parsed = JSON.parse(cached) as Record<string, TokenConfig>;
        if (Object.keys(parsed).length > 0) {
          for (const sym of Object.keys(parsed)) {
            if (!parsed[sym].iconUrl && DEFAULT_TOKENS[sym]?.iconUrl) {
              parsed[sym].iconUrl = DEFAULT_TOKENS[sym].iconUrl;
            }
          }
          setTokens({ ...DEFAULT_TOKENS, ...parsed });
        }
      }

      // Fetch fresh from backend
      const result = await fetchSupportedTokens();
      if (result.success && result.data && result.data.length > 0) {
        const fetched: Record<string, TokenConfig> = {};
        for (const t of result.data) {
          const validChains = t.chains.filter(c => c in CHAINS) as ChainKey[];
          if (validChains.length > 0) {
            fetched[t.symbol] = {
              symbol: t.symbol,
              name: t.name,
              decimals: t.decimals,
              iconColor: t.iconColor,
              iconUrl: DEFAULT_TOKENS[t.symbol]?.iconUrl,
              chains: validChains,
            };
          }
        }
        if (Object.keys(fetched).length > 0) {
          // Merge with DEFAULT_TOKENS so native tokens (S, BNB) are never lost
          const merged = { ...DEFAULT_TOKENS, ...fetched };
          setTokens(merged);
          await AsyncStorage.setItem(CACHE_KEY, JSON.stringify(merged));
        }
      }
    } catch (err) {
      // Fallback stays as DEFAULT_TOKENS or cached
    } finally {
      setLoading(false);
    }
  }, []);

  // Defer network fetch — load cache sync, fetch from backend after 2s
  useEffect(() => {
    // Load cache immediately (fast, no network)
    AsyncStorage.getItem(CACHE_KEY).then((cached) => {
      if (cached) {
        try {
          const parsed = JSON.parse(cached) as Record<string, TokenConfig>;
          if (Object.keys(parsed).length > 0) {
            for (const sym of Object.keys(parsed)) {
              if (!parsed[sym].iconUrl && DEFAULT_TOKENS[sym]?.iconUrl) {
                parsed[sym].iconUrl = DEFAULT_TOKENS[sym].iconUrl;
              }
            }
            setTokens({ ...DEFAULT_TOKENS, ...parsed });
          }
        } catch {}
      }
    });
    // Defer the network call so it doesn't compete with startup
    const timer = setTimeout(() => loadTokens(), 3000);
    return () => clearTimeout(timer);
  }, [loadTokens]);

  const allTokens: Record<string, TokenConfig> = { ...tokens };
  for (const ct of customTokens) {
    if (!allTokens[ct.symbol]) {
      allTokens[ct.symbol] = ct;
    }
  }

  const value: TokenContextValue = {
    tokens: allTokens,
    tokenList: [...Object.values(tokens), ...customTokens.filter(ct => !tokens[ct.symbol])],
    customTokens,
    loading,
    refresh: loadTokens,
    getTokensForChain: (chainKey) => getTokensForChain(allTokens, chainKey),
    getChainsForToken: (symbol) => {
      const token = allTokens[symbol];
      if (!token) return [];
      return token.chains.map(key => ({ ...CHAINS[key], key }));
    },
    importToken,
    removeToken,
  };

  return (
    <TokenContext.Provider value={value}>
      {children}
    </TokenContext.Provider>
  );
}

export function useTokens(): TokenContextValue {
  const ctx = useContext(TokenContext);
  if (!ctx) {
    throw new Error('useTokens must be used within a TokenProvider');
  }
  return ctx;
}
