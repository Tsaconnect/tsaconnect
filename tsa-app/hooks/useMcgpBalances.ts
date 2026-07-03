// hooks/useMcgpBalances.ts
//
// Balances shown on the MCGP buy/sell screens: the user's Sonic USDC balance
// (what they pay/receive) and their spendable premium MCGP balance (what they
// can sell). Human-readable numbers plus the exact string forms for a "Max"
// button that must not lose precision.
import { useCallback, useEffect, useState } from 'react';
import { ethers } from 'ethers';
import { getMcgpBalance } from '@/services/mcgpSwapApi';
import { getWalletBalances } from '@/services/walletApi';
import { getNetwork } from '@/hooks/useNetwork';

export interface McgpBalancesState {
  usdc: number; // human USDC (Sonic)
  mcgpPremium: number; // human spendable MCGP
  mcgpPremiumStr: string; // exact string (18dp) for Max
  loading: boolean;
  refresh: () => void;
}

export function useMcgpBalances(): McgpBalancesState {
  const [usdc, setUsdc] = useState(0);
  const [mcgpPremium, setMcgpPremium] = useState(0);
  const [mcgpPremiumStr, setMcgpPremiumStr] = useState('0');
  const [loading, setLoading] = useState(true);

  const refresh = useCallback(async () => {
    setLoading(true);
    const sonicChainId = getNetwork() === 'testnet' ? 14601 : 146;
    const [mcgpRes, balRes] = await Promise.all([getMcgpBalance(), getWalletBalances(sonicChainId)]);

    if (mcgpRes.success && mcgpRes.data) {
      const raw = mcgpRes.data.premiumBalance || '0';
      try {
        const str = ethers.formatUnits(raw, 18);
        setMcgpPremiumStr(str);
        setMcgpPremium(Number(str));
      } catch {
        setMcgpPremiumStr('0');
        setMcgpPremium(0);
      }
    }
    if (balRes.success && Array.isArray(balRes.data)) {
      const u = balRes.data.find((b) => b.symbol?.toUpperCase() === 'USDC');
      setUsdc(u ? parseFloat(u.balance || '0') || 0 : 0);
    }
    setLoading(false);
  }, []);

  useEffect(() => {
    refresh();
  }, [refresh]);

  return { usdc, mcgpPremium, mcgpPremiumStr, loading, refresh };
}
