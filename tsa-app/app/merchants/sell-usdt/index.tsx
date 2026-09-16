// app/merchants/sell-usdt/index.tsx
//
// Merchant sell-USDT flow — enter an amount, see the fiat payout at the current
// rate, pick a payout method, and confirm the sale. Restyled onto the TSA
// Signature design system (theme tokens + @/components/ui primitives).
//
// FINANCIAL screen: the amount state, the fixed exchangeRate, the
// `Number(amount) * exchangeRate` payout computation (and its `.toLocaleString()`
// formatting), the disabled-while-empty submit gating, and the handleSell
// confirm flow are all preserved verbatim — this is a visual/structural
// restyle only.
import React, { useState } from 'react';
import { View, ScrollView } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useTheme } from '@/theme';
import {
    AccentCard,
    Button,
    Icon,
    Text,
    AmountInput,
    SelectRow,
} from '@/components/ui';

const SellUSDTScreen = () => {
    const { colors, spacing } = useTheme();
    const [amount, setAmount] = useState('');
    const exchangeRate = 1550; // Example exchange rate

    const handleSell = () => {
        // Implementation for selling USDT
        console.log('Processing sale for:', amount);
    };

    return (
        <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
            <ScrollView
                contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}
                keyboardShouldPersistTaps="handled"
            >
                {/* Amount to sell */}
                <View style={{ marginBottom: spacing['2xl'] }}>
                    <AmountInput
                        label="Amount to Sell"
                        placeholder="0.00"
                        value={amount}
                        onChangeText={setAmount}
                        symbol="USDT"
                        symbolPosition="suffix"
                    />
                    <View
                        style={{
                            flexDirection: 'row',
                            justifyContent: 'space-between',
                            marginTop: spacing.sm,
                        }}
                    >
                        <Text variant="caption" color="textMuted">
                            Balance: 2,450.00 USDT
                        </Text>
                        <Text variant="caption" weight="700" color="accent">
                            Sell Max
                        </Text>
                    </View>
                </View>

                {/* You will receive */}
                <View style={{ marginBottom: spacing['2xl'] }}>
                    <Text variant="bodySm" weight="600" color="textSecondary" style={{ marginBottom: spacing.xs }}>
                        You will receive
                    </Text>
                    <AccentCard padding="xl">
                        <Text variant="display" weight="800" color="accent" numeric>
                            ${(Number(amount) * exchangeRate).toLocaleString()}
                        </Text>
                        <View
                            style={{
                                flexDirection: 'row',
                                alignItems: 'center',
                                gap: spacing.xs,
                                marginTop: spacing.sm,
                            }}
                        >
                            <Icon name="trendingUp" size={14} color={colors.accent} />
                            <Text variant="caption" weight="500" color="accent">
                                Rate: 1 USDT = ${exchangeRate.toLocaleString()}
                            </Text>
                        </View>
                    </AccentCard>
                </View>

                {/* Payout method */}
                <View style={{ marginBottom: spacing['2xl'] }}>
                    <SelectRow
                        label="Payout Method"
                        leftIcon="bank"
                        value="Kuda Bank · 0123456789 • John Doe"
                        onPress={() => {}}
                    />
                </View>

                {/* Confirm */}
                <Button
                    title="Confirm Sale"
                    size="lg"
                    onPress={handleSell}
                    disabled={!amount}
                />

                <Text
                    variant="caption"
                    color="textMuted"
                    align="center"
                    style={{ marginTop: spacing.lg, paddingHorizontal: spacing['2xl'] }}
                >
                    By confirming, you agree to the transaction terms and current market rates.
                </Text>
            </ScrollView>
        </SafeAreaView>
    );
};

export default SellUSDTScreen;
