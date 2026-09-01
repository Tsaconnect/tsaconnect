// components/wallet/NoLocalWallet.tsx
//
// Shown wherever a balance would otherwise appear for a wallet this device
// holds no signing key for.
//
// The wording is the point. A zero balance reads as "your wallet is empty" and
// sends the user hunting for missing funds; the account's registered balance
// reads as "TSA is holding my MCGP for me". Neither is true — the MCGP is
// on-chain against a key that lives only on the user's device — so this says
// exactly that and offers the one action that fixes it.
import React from 'react';
import { View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
import { useTheme } from '@/theme';
import { EmptyState } from '@/components/ui';

export interface NoLocalWalletProps {
  /** What the user came here to do, e.g. "swap", "buy MCGP", "pay". */
  action?: string;
}

export function NoLocalWalletNotice({ action = 'continue' }: NoLocalWalletProps) {
  return (
    <EmptyState
      icon="wallet"
      title="Import your wallet"
      subtitle={`Your MCGP is held on-chain by your own wallet. Import its recovery phrase on this device to view your balance and ${action}.`}
      actionLabel="Import wallet"
      onAction={() => router.push('/wallet/seedphrase')}
    />
  );
}

/** Full-screen variant for routes whose entire purpose needs a signing key. */
export function NoLocalWalletScreen({ action }: NoLocalWalletProps) {
  const { colors } = useTheme();
  return (
    <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
      <View style={{ flex: 1, justifyContent: 'center' }}>
        <NoLocalWalletNotice action={action} />
      </View>
    </SafeAreaView>
  );
}
