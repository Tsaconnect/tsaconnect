// app/(productgroup)/confirmation.tsx
//
// Order confirmation / success — shown after a buyer submits payment proof
// (`router.push("/confirmation")` from components/orders/paymentproof.tsx). It
// surfaces the "payment confirmation in progress" state and a single way back to
// the product dashboard. Restyled onto the TSA Signature design system (theme
// tokens + @/components/ui primitives); the legacy COLORS-based
// ConfirmationScreen / PaymentReceivedScreen components are no longer used here.
//
// Logic is preserved verbatim: same in-progress messaging, same large spinner,
// and the same "Return to dashboard" navigation to /products. The
// PaymentReceivedScreen variant remains intentionally dormant (was commented out
// in the legacy wrapper).
import React from 'react';
import { View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
import { useTheme } from '@/theme';
import { AccentCard, Button, Icon, Spinner, Text } from '@/components/ui';
import { MARKETPLACE_TAB } from '@/constants/routes';

const Confirmation = () => {
  const { colors, spacing, radius } = useTheme();

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <View
        style={{
          flex: 1,
          justifyContent: 'center',
          alignItems: 'center',
          padding: spacing.lg,
        }}
      >
        <AccentCard padding="xl" style={{ width: '100%', alignItems: 'center' }}>
          <View
            style={{
              width: 72,
              height: 72,
              borderRadius: radius.pill,
              backgroundColor: colors.infoBg,
              justifyContent: 'center',
              alignItems: 'center',
              marginBottom: spacing.lg,
            }}
          >
            <Icon name="info" size={32} color={colors.info} />
          </View>

          <Text variant="title3" weight="700" align="center" style={{ marginBottom: spacing.sm }}>
            Hang Tight
          </Text>
          <Text
            variant="bodySm"
            color="textSecondary"
            align="center"
            style={{ marginBottom: spacing.xl }}
          >
            Confirmation of payment in progress
          </Text>

          <Spinner size="large" />
        </AccentCard>

        <Button
          title="Return to dashboard"
          variant="primary"
          rightIcon="arrowRight"
          onPress={() => {
            router.push(MARKETPLACE_TAB);
          }}
          style={{ marginTop: spacing.xl, alignSelf: 'stretch' }}
        />
      </View>
    </SafeAreaView>
  );
};

export default Confirmation;
