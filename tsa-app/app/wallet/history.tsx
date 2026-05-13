import { useLocalSearchParams } from 'expo-router';
import WalletHistory from '@/screens/wallet-history';

export default function HistoryScreen() {
  const { chainId, tokenSymbol } = useLocalSearchParams<{ chainId?: string; tokenSymbol?: string }>();
  return (
    <WalletHistory
      chainId={chainId ? Number(chainId) : undefined}
      tokenSymbol={tokenSymbol}
    />
  );
}
