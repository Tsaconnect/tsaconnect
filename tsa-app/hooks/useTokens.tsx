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

const CACHE_KEY = 'tsa-supported-tokens';

export type ChainWithKey = ChainConfig & { key: ChainKey };

interface TokenContextValue {
  tokens: Record<string, TokenConfig>;
  tokenList: TokenConfig[];
  loading: boolean;
  refresh: () => Promise<void>;
  getTokensForChain: (chainKey: ChainKey) => TokenConfig[];
  getChainsForToken: (symbol: string) => ChainWithKey[];
}

const TokenContext = createContext<TokenContextValue | null>(null);

export function TokenProvider({ children }: { children: ReactNode }) {
  const [tokens, setTokens] = useState<Record<string, TokenConfig>>(DEFAULT_TOKENS);
  const [loading, setLoading] = useState(true);

  const loadTokens = useCallback(async () => {
    try {
      // Try cache first for instant UI
      const cached = await AsyncStorage.getItem(CACHE_KEY);
      if (cached) {
        const parsed = JSON.parse(cached) as Record<string, TokenConfig>;
        if (Object.keys(parsed).length > 0) {
          // Merge iconUrl from defaults in case cache is missing them
          for (const sym of Object.keys(parsed)) {
            if (!parsed[sym].iconUrl && DEFAULT_TOKENS[sym]?.iconUrl) {
              parsed[sym].iconUrl = DEFAULT_TOKENS[sym].iconUrl;
            }
          }
          setTokens(parsed);
        }
      }

      // Fetch fresh from backend
      const result = await fetchSupportedTokens();
      if (result.success && result.data && result.data.length > 0) {
        const fetched: Record<string, TokenConfig> = {};
        for (const t of result.data) {
          // Only include chains we actually support in CHAINS
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
          setTokens(fetched);
          await AsyncStorage.setItem(CACHE_KEY, JSON.stringify(fetched));
        }
      }
    } catch (err) {
      console.error('Load tokens error:', err);
      // Fallback stays as DEFAULT_TOKENS or cached
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    loadTokens();
  }, [loadTokens]);

  const value: TokenContextValue = {
    tokens,
    tokenList: Object.values(tokens),
    loading,
    refresh: loadTokens,
    getTokensForChain: (chainKey) => getTokensForChain(tokens, chainKey),
    getChainsForToken: (symbol) => {
      const token = tokens[symbol];
      if (!token) return [];
      return token.chains.map(key => ({ ...CHAINS[key], key }));
    },
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
