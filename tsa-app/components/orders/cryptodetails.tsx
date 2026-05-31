// components/orders/cryptodetails.tsx
//
// Crypto payment details — shows the deposit wallet address the buyer must send
// crypto to, a copy-to-clipboard control, the network/symbol warning, and the
// Proceed CTA into the payment-proof flow. Migrated onto the TSA Signature
// design system (@/theme tokens + @/components/ui primitives) off the legacy
// COLORS/SIZES styling.
//
// FINANCIAL screen: the wallet address display, the clipboard copy, the
// "send only {symbol}-{network}" guidance, and the Proceed navigation
// (router.push to /paymentproof with { id }) are preserved verbatim — this is a
// visual/structural restyle only. The clipboard copy now goes through the
// CopyableField primitive (expo-clipboard + toast), which is cross-platform
// rather than the prior Android-only ToastAndroid.
import React from "react";
import { ScrollView, View } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { router } from "expo-router";
import { useTheme } from "@/theme";
import {
  AccentCard,
  Button,
  CopyableField,
  Icon,
  Text,
} from "@/components/ui";

const CryptoDetails = ({
  walletAddress,
  symbol,
  network,
  id,
}: {
  walletAddress?: string;
  symbol?: string;
  network?: string;
  id?: string;
}) => {
  const { colors, spacing } = useTheme();

  return (
    <SafeAreaView
      style={{ flex: 1, backgroundColor: colors.background }}
      edges={["top"]}
    >
      <ScrollView
        contentContainerStyle={{
          padding: spacing.lg,
          paddingBottom: spacing["4xl"],
          flexGrow: 1,
        }}
      >
        <Text variant="overline" color="textMuted">
          DEPOSIT ADDRESS
        </Text>

        <CopyableField
          value={walletAddress ?? ''}
          label="Wallet address"
          toastMessage="Copied!"
          style={{ marginTop: spacing.sm }}
        />

        <AccentCard
          padding="md"
          railColor={colors.warning}
          style={{ marginTop: spacing.lg, backgroundColor: colors.warningBg }}
        >
          <View
            style={{
              flexDirection: "row",
              gap: spacing.sm,
              alignItems: "flex-start",
            }}
          >
            <Icon name="warning" size={16} color={colors.warning} />
            <Text variant="bodySm" color="textSecondary" style={{ flex: 1 }}>
              Send only {symbol}-{network} network to the wallet address above
            </Text>
          </View>
        </AccentCard>

        <View style={{ flex: 1 }} />

        <Button
          title="Proceed"
          size="lg"
          onPress={() => {
            router.push({ pathname: "/paymentproof", params: { id } });
          }}
          style={{ marginTop: spacing["2xl"] }}
        />
      </ScrollView>
    </SafeAreaView>
  );
};

export default CryptoDetails;
