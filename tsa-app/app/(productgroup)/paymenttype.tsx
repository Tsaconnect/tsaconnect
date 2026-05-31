// app/(productgroup)/paymenttype.tsx
//
// Payment method selection screen — reads the checkout delivery params off the
// route and hands them to <PaymentSelection> (Fiat / Crypto picker + order
// placement). Restyled onto the TSA Signature design system (theme tokens +
// @/components/ui primitives); previously a bare <View> wrapper.
//
// FINANCIAL screen: the route params (address/state/city/country/phoneNumber/
// totalAmount/fullName) and their forwarding to PaymentSelection — which owns
// the payment-method options, selection, order placement and next-step
// navigation — are preserved verbatim. Visual/structural restyle only.
import React from "react";
import { ScrollView, View } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { useLocalSearchParams } from "expo-router";
import { useTheme } from "@/theme";
import { Text } from "@/components/ui";
import PaymentSelection from "../../components/orders/PaymentType";

const PaymentTypeScren = () => {
  const { colors, spacing } = useTheme();
  const { address, state, city, country, phoneNumber, totalAmount, fullName } =
    useLocalSearchParams<{
      address: string;
      state: string;
      city: string;
      country: string;
      phoneNumber: string;
      totalAmount: string;
      fullName: string;
    }>();

  return (
    <SafeAreaView
      style={{ flex: 1, backgroundColor: colors.background }}
      edges={["top"]}
    >
      <ScrollView
        contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing["4xl"] }}
      >
        <View style={{ marginBottom: spacing.lg }}>
          <Text variant="title1" weight="700">
            Payment method
          </Text>
          <Text variant="bodySm" color="textSecondary" style={{ marginTop: spacing.xs }}>
            Choose how to pay for your order.
          </Text>
        </View>

        <PaymentSelection
          address={address}
          state={state}
          city={city}
          country={country}
          phoneNumber={phoneNumber}
          totalAmount={totalAmount}
          fullName={fullName}
        />
      </ScrollView>
    </SafeAreaView>
  );
};

export default PaymentTypeScren;
