import React, { useState } from 'react';
import { View, Text, TextInput, Pressable, StyleSheet, KeyboardAvoidingView, Platform } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';

const BuyUSDTScreen = () => {
    const [amount, setAmount] = useState('');
    const [exchangeRate, setExchangeRate] = useState('1.01');

    const calculatedUSDT = amount ? (parseFloat(amount) / parseFloat(exchangeRate)).toFixed(2) : '0.00';

    const handlePurchase = () => {
        // Implement purchase logic
        console.log(`Purchasing ${calculatedUSDT} USDT for $${amount}`);
    };

    return (
        <SafeAreaView style={styles.container}>
            <KeyboardAvoidingView
                behavior={Platform.OS === 'ios' ? 'padding' : 'height'}
                style={styles.content}
            >
                <View style={styles.header}>
                    <Text style={styles.title}>Buy USDT</Text>
                    <Text style={styles.subtitle}>Enter the amount of USD you want to spend</Text>
                </View>

                <View style={styles.inputWrapper}>
                    <Text style={styles.label}>Spend (USD)</Text>
                    <View style={styles.inputContainer}>
                        <Text style={styles.currencySymbol}>$</Text>
                        <TextInput
                            style={styles.input}
                            placeholder="0.00"
                            keyboardType="decimal-pad"
                            value={amount}
                            onChangeText={setAmount}
                            autoFocus
                        />
                    </View>
                </View>

                <View style={styles.infoCard}>
                    <View style={styles.infoRow}>
                        <Text style={styles.infoLabel}>Exchange Rate</Text>
                        <Text style={styles.infoValue}>1 USDT ≈ ${exchangeRate}</Text>
                    </View>
                    <View style={[styles.infoRow, styles.totalRow]}>
                        <Text style={styles.totalLabel}>You'll Receive</Text>
                        <Text style={styles.totalValue}>{calculatedUSDT} USDT</Text>
                    </View>
                </View>

                <Pressable
                    style={({ pressed }) => [
                        styles.button,
                        !amount && styles.buttonDisabled,
                        pressed && { opacity: 0.7 }
                    ]}
                    onPress={handlePurchase}
                    disabled={!amount}
                >
                    <Text style={styles.buttonText}>Continue to Payment</Text>
                </Pressable>
            </KeyboardAvoidingView>
        </SafeAreaView>
    );
};

const styles = StyleSheet.create({
    container: {
        flex: 1,
        backgroundColor: '#FFFFFF',
    },
    content: {
        flex: 1,
        paddingHorizontal: 20,
        paddingTop: 40,
    },
    header: {
        marginBottom: 32,
    },
    title: {
        fontSize: 28,
        fontWeight: '700',
        color: '#1A1A1A',
    },
    subtitle: {
        fontSize: 16,
        color: '#666666',
        marginTop: 8,
    },
    inputWrapper: {
        marginBottom: 24,
    },
    label: {
        fontSize: 14,
        fontWeight: '600',
        color: '#666666',
        marginBottom: 8,
    },
    inputContainer: {
        flexDirection: 'row',
        alignItems: 'center',
        borderBottomWidth: 2,
        borderBottomColor: '#A52A2A',
        paddingBottom: 8,
    },
    currencySymbol: {
        fontSize: 32,
        fontWeight: '600',
        color: '#1A1A1A',
        marginRight: 8,
    },
    input: {
        flex: 1,
        fontSize: 32,
        fontWeight: '600',
        color: '#1A1A1A',
    },
    infoCard: {
        backgroundColor: '#F8F9FA',
        borderRadius: 12,
        padding: 16,
        marginBottom: 32,
    },
    infoRow: {
        flexDirection: 'row',
        justifyContent: 'space-between',
        marginBottom: 12,
    },
    infoLabel: {
        fontSize: 14,
        color: '#666666',
    },
    infoValue: {
        fontSize: 14,
        fontWeight: '500',
        color: '#1A1A1A',
    },
    totalRow: {
        marginTop: 8,
        paddingTop: 16,
        borderTopWidth: 1,
        borderTopColor: '#EEEEEE',
        marginBottom: 0,
    },
    totalLabel: {
        fontSize: 16,
        fontWeight: '600',
        color: '#1A1A1A',
    },
    totalValue: {
        fontSize: 18,
        fontWeight: '700',
        color: '#00C853',
    },
    button: {
        backgroundColor: '#A52A2A',
        height: 56,
        borderRadius: 28,
        justifyContent: 'center',
        alignItems: 'center',
        marginTop: 'auto',
        marginBottom: 20,
    },
    buttonDisabled: {
        backgroundColor: '#E0E0E0',
    },
    buttonText: {
        color: '#FFFFFF',
        fontSize: 18,
        fontWeight: '600',
    },
});

export default BuyUSDTScreen;

