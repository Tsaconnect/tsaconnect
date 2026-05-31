// components/onboarding/order.tsx
//
// First-run onboarding intro slide (the "order" / final slide, index 3). Migrated
// onto the TSA Signature design system: theme tokens (colors / spacing) via useTheme
// and the @/components/ui Text primitive instead of the legacy constants/theme
// (COLORS/SIZES) and raw RN Text/StyleSheet. The slide still owns the full screen
// (its parent app/order.tsx only wraps it with the auth redirect), so it renders
// its own themed SafeAreaView background. Behavior is unchanged: same default export,
// same handleNext/handleSkip both routing to '/login', same onboarding3 artwork, and
// the same pager widget (SkipNextButton with index={3}, which hides Skip on the last
// slide and renders the Progress/Dots indicators). The legacy absolute SIZES.height
// positioning is replaced by a flex column (heading → flexing artwork → pinned footer).
import React from 'react';
import { Image, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
import { useTheme } from '@/theme';
import { Text } from '@/components/ui';
import { SkipNextButton } from '../buttons/NextSkip';

const Order = () => {
  const { colors, spacing } = useTheme();

  function handleNext() {
    router.push('/login');
  }
  function handleSkip() {
    router.push('/login');
  }

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <View style={{ flex: 1, alignItems: 'center', paddingHorizontal: spacing.lg }}>
        <View style={{ alignItems: 'center', marginTop: spacing['4xl'] }}>
          <Text variant="title1" weight="700" color="accent" align="center">
            Order for services
          </Text>
          <Text variant="title1" weight="700" color="accent" align="center">
            anytime, anywhere
          </Text>
        </View>

        <View style={{ flex: 1, width: '100%', alignItems: 'center', justifyContent: 'center' }}>
          <Image
            source={require('../../assets/images/onboarding3.png')}
            style={{ width: '90%', height: '100%' }}
            resizeMode="contain"
          />
        </View>

        <View style={{ width: '100%', paddingBottom: spacing.lg }}>
          <SkipNextButton handleNext={handleNext} handleSkip={handleSkip} index={3} />
        </View>
      </View>
    </SafeAreaView>
  );
};

export default Order;
