// hooks/useMcgpBalances.ts
//
// Balances shown on the MCGP buy/sell screens: the user's Sonic USDC balance
// (what they pay/receive) and their spendable premium MCGP balance (what they
// can sell). Human-readable numbers plus the exact string forms for a "Max"
// button that must not lose precision.
//
// Everything here is read from the chain for the wallet THIS DEVICE can sign
// for. When there is no such wallet the hook reports `walletMissing` and shows
// nothing — never a zero, which reads as an empty wallet, and never the
// account's registered balance, which reads as us holding the user's MCGP.
import { useCallback, useEffect, useState } from 'react';
import { ethers } from 'ethers';
import { getMcgpBalance } from '@/services/mcgpSwapApi';
import { getWalletBalances, normalizeWalletBalances } from '@/services/walletApi';
import { getNetwork } from '@/hooks/useNetwork';
import { getSignableWallet } from '@/services/wallet';

export interface McgpBalancesState {
  usdc: number; // human USDC (Sonic)
  mcgpPremium: number; // human spendable MCGP
  mcgpPremiumStr: string; // exact string (18dp) for Max
  loading: boolean;
  /** No signing key on this device — prompt to import, do not render a balance. */
  walletMissing: boolean;
  refresh: () => void;
}

export function useMcgpBalances(): McgpBalancesState {
  const [usdc, setUsdc] = useState(0);
  const [mcgpPremium, setMcgpPremium] = useState(0);
  const [mcgpPremiumStr, setMcgpPremiumStr] = useState('0');
  const [loading, setLoading] = useState(true);
  const [walletMissing, setWalletMissing] = useState(false);

  const refresh = useCallback(async () => {
    setLoading(true);

    const wallet = await getSignableWallet();
    if (!wallet) {
      setWalletMissing(true);
      setUsdc(0);
      setMcgpPremium(0);
      setMcgpPremiumStr('0');
      setLoading(false);
      return;
    }
    setWalletMissing(false);

    const sonicChainId = getNetwork() === 'testnet' ? 14601 : 146;
    const [mcgpRes, balRes] = await Promise.all([
      getMcgpBalance(),
      getWalletBalances(sonicChainId, wallet),
    ]);

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
    if (balRes.success && balRes.data) {
      // /wallet/balances returns { balances: { <chain>: { <symbol>: {...} } } },
      // not a flat array — normalize before reading Sonic USDC.
      const flat = normalizeWalletBalances(balRes.data, 'sonic');
      const u = flat.find(
        (b) => b.symbol?.toUpperCase() === 'USDC' && b.chainKey === 'sonic',
      );
      setUsdc(u ? parseFloat(u.balance || '0') || 0 : 0);
    }
    setLoading(false);
  }, []);

  useEffect(() => {
    refresh();
  }, [refresh]);

  return { usdc, mcgpPremium, mcgpPremiumStr, loading, walletMissing, refresh };
}
