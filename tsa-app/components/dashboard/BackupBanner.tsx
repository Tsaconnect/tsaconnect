// components/dashboard/BackupBanner.tsx
//
// Surfaces a "set up your wallet" prompt (no wallet) or a "back up your seed
// phrase" reminder (wallet exists, not backed up / not dismissed). Migrated onto
// the TSA Signature design system — the getActiveWallet check, all AsyncStorage
// reads/writes (seedPhraseBackedUp / backupBannerDismissed / backupBannerRemindAt),
// the remind/never Alert flow, and navigation routes are preserved verbatim.
import React, { useState, useEffect } from 'react';
import { View, Alert } from 'react-native';
import { router } from 'expo-router';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { getActiveWallet } from '@/services/wallet';
import { useTheme } from '@/theme';
import { AccentCard, Icon, IconButton, PressableScale, Text } from '@/components/ui';

export const BackupBanner: React.FC = () => {
  const { colors, spacing } = useTheme();
  const [bannerType, setBannerType] = useState<'none' | 'no_wallet' | 'backup'>(
    'none',
  );

  useEffect(() => {
    (async () => {
      try {
        const walletAddress = await getActiveWallet();

        // No wallet at all — prompt to create one
        if (!walletAddress) {
          setBannerType('no_wallet');
          return;
        }

        // Wallet exists — check if seed phrase backed up
        const backedUp = await AsyncStorage.getItem('seedPhraseBackedUp');
        if (backedUp === 'true') return;
        const dismissed = await AsyncStorage.getItem('backupBannerDismissed');
        if (dismissed === 'true') return;
        const remindAt = await AsyncStorage.getItem('backupBannerRemindAt');
        if (remindAt && Date.now() < parseInt(remindAt, 10)) return;
        setBannerType('backup');
      } catch {}
    })();
  }, []);

  if (bannerType === 'none') return null;

  const handleRemindTomorrow = async () => {
    setBannerType('none');
    await AsyncStorage.setItem(
      'backupBannerRemindAt',
      (Date.now() + 86400000).toString(),
    );
  };

  const handleNever = async () => {
    setBannerType('none');
    await AsyncStorage.setItem('backupBannerDismissed', 'true');
  };

  // No wallet banner
  if (bannerType === 'no_wallet') {
    return (
      <PressableScale onPress={() => router.push('/wallet/home')} style={{ marginBottom: spacing.md }}>
        <AccentCard padding="md" railColor={colors.info}>
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
            <Icon name="wallet" size={20} color={colors.info} />
            <Text variant="bodySm" weight="500" style={{ flex: 1 }}>
              Set up your wallet to make purchases and receive cashback
            </Text>
            <Icon name="chevronRight" size={20} color={colors.info} />
          </View>
        </AccentCard>
      </PressableScale>
    );
  }

  // Backup banner
  return (
    <View style={{ marginBottom: spacing.md }}>
      <AccentCard padding="md" railColor={colors.warning}>
        <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
          <PressableScale
            onPress={() => router.push('/wallet/seedphrase')}
            style={{ flex: 1, flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}
          >
            <Icon name="shield" size={20} color={colors.warning} />
            <Text variant="bodySm" weight="500" style={{ flex: 1 }}>
              Back up your seed phrase to protect your funds
            </Text>
          </PressableScale>
          <IconButton
            icon="close"
            size={18}
            color={colors.warning}
            accessibilityLabel="Dismiss backup reminder"
            onPress={() =>
              Alert.alert(
                'Dismiss Reminder',
                'When would you like to be reminded?',
                [
                  { text: 'Remind Tomorrow', onPress: handleRemindTomorrow },
                  { text: 'Never', style: 'destructive', onPress: handleNever },
                  { text: 'Cancel', style: 'cancel' },
                ],
              )
            }
          />
        </View>
      </AccentCard>
    </View>
  );
};

export default BackupBanner;
