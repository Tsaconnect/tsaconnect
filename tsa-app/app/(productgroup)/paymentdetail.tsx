// app/(productgroup)/paymentdetail.tsx
//
// Payment details / instructions — shows the merchant bank account the buyer
// must pay into, then proceeds to upload payment proof. Restyled onto the TSA
// Signature design system (@/theme + @/components/ui).
//
// FINANCIAL: amounts, payment details and the Proceed → /paymentproof flow are
// preserved verbatim. This screen previously delegated to
// components/orders/bankdetails.tsx (legacy COLORS/SIZES); its only other
// consumer, screens/sendFiat.tsx, has since been deleted, so that component now
// has no callers. The bank-detail presentation is inlined here on design-system
// primitives — the params read
// (accountNumber/accountName/bankName/id) and the navigation
// (router.push({ pathname: "/paymentproof", params: { id } })) are identical.
import React from 'react';
import { ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router, useLocalSearchParams } from 'expo-router';
import { useTheme } from '@/theme';
import {
  BottomActionBar,
  Button,
  Card,
  CopyableField,
  Icon,
  Text,
} from '@/components/ui';

const PaymentDetails = () => {
  const { accountNumber, accountName, bankName, id } = useLocalSearchParams();
  const { colors, spacing } = useTheme();

  const bankNameStr = (Array.isArray(bankName) ? bankName[0] : bankName) || '';
  const accountNumberStr = (Array.isArray(accountNumber) ? accountNumber[0] : accountNumber) || '';
  const accountNameStr = (Array.isArray(accountName) ? accountName[0] : accountName) || '';

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <ScrollView
        contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}
        keyboardShouldPersistTaps="handled"
      >
        <Text variant="bodySm" color="textSecondary" style={{ marginBottom: spacing.lg }}>
          Transfer to the account below, then tap Proceed to upload your payment proof.
        </Text>

        <Card>
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm, marginBottom: spacing.md }}>
            <Icon name="bank" size={18} color={colors.accent} />
            <Text variant="overline" color="textMuted">
              BANK ACCOUNT
            </Text>
          </View>

          <View style={{ gap: spacing.sm }}>
            <CopyableField
              label="Bank Name"
              value={bankNameStr || 'N/A'}
              display={bankNameStr || 'N/A'}
              toastMessage="Bank name copied"
            />
            <CopyableField
              label="Account Number"
              value={accountNumberStr || 'N/A'}
              display={accountNumberStr || 'N/A'}
              toastMessage="Account number copied"
            />
            <CopyableField
              label="Account Name"
              value={accountNameStr || 'N/A'}
              display={accountNameStr || 'N/A'}
              toastMessage="Account name copied"
            />
          </View>
        </Card>
      </ScrollView>

      <BottomActionBar>
        <Button
          title="Proceed"
          size="lg"
          onPress={() => {
            router.push({ pathname: '/paymentproof', params: { id } });
          }}
        />
      </BottomActionBar>
    </SafeAreaView>
  );
};

export default PaymentDetails;
