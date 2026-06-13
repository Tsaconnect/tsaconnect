// components/onboarding/products.tsx
//
// First-run onboarding intro slide (the "products" slide, index 1). Migrated onto
// the TSA Signature design system: theme tokens (colors / spacing) via useTheme and
// the @/components/ui Text primitive instead of the legacy constants/theme
// (COLORS/SIZES) and raw RN Text/StyleSheet. The slide owns the full screen, so it
// renders its own themed SafeAreaView background. Behavior is unchanged: same default
// export, same handleNext routing to '/payments' and handleSkip routing to '/login',
// the same onboarding1 artwork, and the same pager widget (SkipNextButton with
// index={1}, which renders the Progress/Dots indicators and keeps the Skip control).
// The legacy absolute SIZES.height positioning is replaced by a flex column
// (heading → flexing artwork → pinned footer), mirroring the migrated payments/order
// slides.
import React from 'react';
import { Image, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
import { useTheme } from '@/theme';
import { Text } from '@/components/ui';
import { SkipNextButton } from '../buttons/NextSkip';

const Products = () => {
  const { colors, spacing } = useTheme();

  function handleNext() {
    router.push('/payments');
  }
  function handleSkip() {
    router.push('/login');
  }

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <View style={{ flex: 1, alignItems: 'center', paddingHorizontal: spacing.lg }}>
        <View style={{ alignItems: 'center', marginTop: spacing['4xl'] }}>
          <Text variant="title1" weight="700" color="accent" align="center">
            Buy and sell your
          </Text>
          <Text variant="title1" weight="700" color="accent" align="center">
            Product
          </Text>
        </View>

        <View style={{ flex: 1, width: '100%', alignItems: 'center', justifyContent: 'center' }}>
          <Image
            source={require('../../assets/images/onboarding1.png')}
            style={{ width: '97%', height: '100%' }}
            resizeMode="contain"
          />
        </View>

        <View style={{ width: '100%', paddingBottom: spacing.lg }}>
          <SkipNextButton handleNext={handleNext} handleSkip={handleSkip} index={1} />
        </View>
      </View>
    </SafeAreaView>
  );
};

export default Products;
