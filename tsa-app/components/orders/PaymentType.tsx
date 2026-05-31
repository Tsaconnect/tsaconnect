// components/orders/PaymentType.tsx
//
// Checkout payment-method selection — pick Fiat (bank transfer) or Crypto
// (USDT / MCGP), then place the order and route to the matching payment-detail
// screen. Restyled onto the TSA Signature design system (theme tokens +
// @/components/ui primitives); previously used legacy COLORS/SIZES + raw
// TouchableOpacity/TextInput.
//
// FINANCIAL component: the payment-method options, selection state, order
// placement (POST /order), payment-method lookup (GET /payment-methods), the
// tTy "system fee" gating, the alerts, and the next-step navigation to
// /paymentdetail and /cryptodetails (with their exact params) are all preserved
// verbatim — this is a visual/structural restyle only. Props interface and the
// default export are unchanged.
import { Image, View } from "react-native";
import React, { useState } from "react";
import { baseUrl } from "../../constants/api/apiClient";
import { useAuth } from "../../AuthContext/AuthContext";
import { router } from "expo-router";
import axios from "axios";
import { useTheme } from "@/theme";
import {
  Button,
  Icon,
  Input,
  PressableScale,
  SegmentedControl,
  Text,
} from "@/components/ui";

interface PaymentSelectionProps {
  address: string;
  state: string;
  city: string;
  country: string;
  phoneNumber: string;
  totalAmount: string;
  fullName: string;
}

const PaymentSelection: React.FC<PaymentSelectionProps> = ({
  address,
  state,
  city,
  country,
  phoneNumber,
  totalAmount,
  fullName,
}) => {
  const { colors, spacing, radius } = useTheme();
  const [selectedPaymentType, setSelectedPaymentType] = useState("Fiat");
  const [amount, setAmount] = useState(totalAmount);
  const { token, tTy } = useAuth();
  const [currency, setCurrency] = useState("");
  const [countUSDT, setCountUSDT] = useState(0);
  const [countMCGP, setCountMCGP] = useState(0);

  const placeOrder = async (payLoad: any) => {
    try {
      const response = await axios.post(`${baseUrl}/order`, payLoad, {
        headers: {
          Authorization: `${token}`,
          "Content-Type": "application/json",
        },
      });
      if (response.status === 201) {
        return { data: response.data, message: "success", success: true };
      }
    } catch (error: any) {
      return {
        data: "",
        message: error.response?.data?.message,
        success: false,
      };
    }
  };
  const paymentMethods = async () => {
    try {
      const response = await axios.get(`${baseUrl}/payment-methods`, {
        headers: {
          Authorization: `${token}`,
        },
      });
      if (response.status === 200) {
        return { data: response.data, message: "success", success: true };
      }
    } catch (error: any) {
      return {
        data: "",
        message: error.response?.data?.message,
        success: false,
      };
    }
  };

  const ctaTitle = tTy ? "Pay System Fee" : "Buy Product";

  // Selectable crypto tile — mirrors the RadioRow look so we can show the
  // brand token logo (not in the Icon registry) while staying on theme tokens.
  const CryptoOption: React.FC<{
    symbol: string;
    logo: any;
    onPress: () => void;
  }> = ({ symbol, logo, onPress }) => {
    const selected = currency === symbol;
    return (
      <PressableScale
        onPress={onPress}
        accessibilityRole="radio"
        accessibilityState={{ selected }}
        style={{
          flexDirection: "row",
          alignItems: "center",
          gap: spacing.md,
          padding: spacing.md,
          borderRadius: radius.md,
          borderWidth: 1.5,
          borderColor: selected ? colors.accent : colors.border,
          backgroundColor: selected ? colors.accentMuted : colors.surface,
        }}
      >
        <Image
          source={logo}
          style={{ width: 28, height: 28 }}
          resizeMode="contain"
        />
        <Text variant="bodyMed" weight="600" style={{ flex: 1 }}>
          {symbol}
        </Text>
        {selected && <Icon name="checkCircle" size={22} color={colors.success} />}
      </PressableScale>
    );
  };

  return (
    <View style={{ gap: spacing.lg }}>
      <SegmentedControl
        segments={[
          { key: "Fiat", label: "Fiat" },
          { key: "Crypto", label: "Crypto" },
        ]}
        value={selectedPaymentType}
        onChange={setSelectedPaymentType}
      />

      <Input
        label="Amount"
        placeholder="Enter Amount"
        value={amount}
        onChangeText={setAmount}
        keyboardType="numeric"
        editable={false}
      />

      {selectedPaymentType === "Fiat" && (
        <View style={{ gap: spacing.lg }}>
          <PressableScale
            accessibilityRole="radio"
            accessibilityState={{ selected: true }}
            style={{
              flexDirection: "row",
              alignItems: "center",
              gap: spacing.md,
              padding: spacing.md,
              borderRadius: radius.md,
              borderWidth: 1.5,
              borderColor: colors.accent,
              backgroundColor: colors.accentMuted,
            }}
          >
            <Image
              source={require("../../assets/icons/bank.png")}
              style={{ width: 28, height: 28 }}
              resizeMode="contain"
            />
            <Text variant="bodyMed" weight="600">
              Bank Transfer
            </Text>
          </PressableScale>

          <Button
            title={ctaTitle}
            onPress={async () => {
              const result: any = await placeOrder({
                deliveryAddress: {
                  address,
                  state,
                  city,
                  country,
                  phoneNumber,
                  fullName,
                },
                paymentMethod: "fiat",
              });
              //@ts-ignore
              if (result.success) {
                router.push({
                  pathname: "/paymentdetail",
                  params: {
                    bankName: result.data.paymentDetails.bankName,
                    accountName: result.data.paymentDetails.accountName,
                    accountNumber: result.data.paymentDetails.accountNumber,
                    id: result.data.id,
                  },
                });
              } else {
                alert(result.message);
              }
            }}
            /*  disabled={loading} */
          />
        </View>
      )}

      {selectedPaymentType === "Crypto" && (
        <View style={{ gap: spacing.md }}>
          <CryptoOption
            symbol="USDT"
            logo={require("../../assets/icons/usdt.png")}
            onPress={() => {
              if (countUSDT > 0) {
                setCurrency("");
                setCountUSDT(0);
                setCountMCGP(0);
              } else {
                setCountUSDT(1);
                setCurrency("USDT");
              }
            }}
          />
          <CryptoOption
            symbol="MCGP"
            logo={require("../../assets/digital/mcgp.png")}
            onPress={() => {
              if (countMCGP > 0) {
                setCurrency("");
                setCountMCGP(0);
                setCountUSDT(0);
              } else {
                setCountMCGP(1);
                setCurrency("MCGP");
              }
            }}
          />

          <Button
            title={ctaTitle}
            style={{ marginTop: spacing.sm }}
            onPress={async () => {
              if (!currency) {
                return alert("Please select currency");
              }
              const response: any = await paymentMethods();
              const result: any = await placeOrder({
                deliveryAddress: {
                  address,
                  state,
                  city,
                  country,
                  phoneNumber,
                  fullName,
                },
                paymentMethod: "crypto",
              });
              if (result.success) {
                if (response.success && response.data.length > 0) {
                  const paymentDetails = response.data.find(
                    (method: any) => method.symbol === currency
                  );

                  router.push({
                    pathname: "/cryptodetails",
                    params: {
                      walletAddress: paymentDetails.walletAddress,
                      network: paymentDetails.network,
                      symbol: paymentDetails.symbol,
                      id: result.data.id,
                    },
                  });
                }
              } else {
                alert(result.message);
              }
            }}
          />
        </View>
      )}
    </View>
  );
};

export default PaymentSelection;
