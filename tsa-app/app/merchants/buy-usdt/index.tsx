// app/merchants/buy-usdt/index.tsx
//
// Merchant buy-USDT flow — enter a USD spend amount, see the live exchange-rate
// breakdown, and continue to payment. Restyled onto the TSA Signature design
// system (theme tokens + @/components/ui primitives).
//
// FINANCIAL screen: the amount/rate state, the calculatedUSDT computation
// (parseFloat(amount) / parseFloat(exchangeRate)), the disabled gating on
// `!amount`, and the handlePurchase submit/confirm flow are preserved verbatim —
// this is a visual/structural restyle only.
import React, { useState } from 'react';
import { KeyboardAvoidingView, Platform, ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useTheme } from '@/theme';
import { useKeyboardHeight } from '@/hooks/useKeyboardHeight';
import {
    AmountInput,
    Button,
    BottomActionBar,
    Card,
    SummaryCard,
    SummaryRow,
    Text,
} from '@/components/ui';

const BuyUSDTScreen = () => {
    const { colors, spacing } = useTheme();
    const kbHeight = useKeyboardHeight();
    const [amount, setAmount] = useState('');
    const [exchangeRate, setExchangeRate] = useState('1.01');

    const calculatedUSDT = amount ? (parseFloat(amount) / parseFloat(exchangeRate)).toFixed(2) : '0.00';

    const handlePurchase = () => {
        // Implement purchase logic
        console.log(`Purchasing ${calculatedUSDT} USDT for $${amount}`);
    };

    return (
        <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
            <KeyboardAvoidingView
                behavior={Platform.OS === 'ios' ? 'padding' : 'height'}
                style={{ flex: 1 }}
            >
                <ScrollView
                    contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] + kbHeight }}
                    keyboardShouldPersistTaps="handled"
                >
                    <View style={{ marginBottom: spacing['2xl'] }}>
                        <Text variant="bodySm" color="textSecondary">
                            Enter the amount of USD you want to spend
                        </Text>
                    </View>

                    <AmountInput
                        label="Spend (USD)"
                        symbol="$"
                        placeholder="0.00"
                        value={amount}
                        onChangeText={setAmount}
                        autoFocus
                        containerStyle={{ marginBottom: spacing.xl }}
                    />

                    <Card padding="lg">
                        <SummaryCard dividerBeforeLast>
                            <SummaryRow label="Exchange Rate" value={`1 USDT ≈ $${exchangeRate}`} />
                            <SummaryRow
                                label="You'll Receive"
                                value={`${calculatedUSDT} USDT`}
                                valueColor="success"
                                total
                            />
                        </SummaryCard>
                    </Card>
                </ScrollView>

                <BottomActionBar>
                    <Button
                        title="Continue to Payment"
                        onPress={handlePurchase}
                        disabled={!amount}
                    />
                </BottomActionBar>
            </KeyboardAvoidingView>
        </SafeAreaView>
    );
};

export default BuyUSDTScreen;
