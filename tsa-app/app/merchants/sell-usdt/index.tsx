import React, { useState } from 'react';
import { View, Text, TextInput, TouchableOpacity, ScrollView, StyleSheet } from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import { useRouter } from 'expo-router';
import { SafeAreaView } from "react-native-safe-area-context"
const SellUSDTScreen = () => {
    const router = useRouter();
    const [amount, setAmount] = useState('');
    const exchangeRate = 1550; // Example exchange rate

    const handleSell = () => {
        // Implementation for selling USDT
        console.log('Processing sale for:', amount);
    };

    return (
        <SafeAreaView style={styles.container}>
            <View style={styles.header}>
                <TouchableOpacity onPress={() => router.back()} style={styles.backButton}>
                    <Ionicons name="arrow-back" size={24} color="#111827" />
                </TouchableOpacity>
                <Text style={styles.headerTitle}>Sell USDT</Text>
            </View>

            <ScrollView style={styles.content} contentContainerStyle={styles.scrollContent}>
                <View style={styles.section}>
                    <Text style={styles.label}>Amount to Sell</Text>
                    <View style={styles.inputContainer}>
                        <TextInput
                            style={styles.input}
                            placeholder="0.00"
                            placeholderTextColor="#9CA3AF"
                            keyboardType="decimal-pad"
                            value={amount}
                            onChangeText={setAmount}
                        />
                        <View style={styles.currencyBadge}>
                            <Text style={styles.currencyText}>USDT</Text>
                        </View>
                    </View>
                    <View style={styles.limitContainer}>
                        <Text style={styles.balanceText}>Balance: 2,450.00 USDT</Text>
                        <TouchableOpacity>
                            <Text style={styles.sellMaxText}>Sell Max</Text>
                        </TouchableOpacity>
                    </View>
                </View>

                <View style={styles.section}>
                    <Text style={styles.label}>You will receive</Text>
                    <View style={styles.receiveContainer}>
                        <Text style={styles.receiveAmount}>
                            ${(Number(amount) * exchangeRate).toLocaleString()}
                        </Text>
                        <View style={styles.rateContainer}>
                            <Ionicons name="trending-up" size={14} color="#1D4ED8" />
                            <Text style={styles.rateText}>
                                Rate: 1 USDT = ${exchangeRate.toLocaleString()}
                            </Text>
                        </View>
                    </View>
                </View>

                <View style={styles.section}>
                    <Text style={styles.label}>Payout Method</Text>
                    <TouchableOpacity style={styles.payoutMethodContainer}>
                        <View style={styles.payoutLeft}>
                            <View style={styles.payoutIconContainer}>
                                <Ionicons name="business" size={20} color="#374151" />
                            </View>
                            <View style={styles.payoutDetails}>
                                <Text style={styles.payoutTitle}>Kuda Bank</Text>
                                <Text style={styles.payoutSubtitle}>0123456789 • John Doe</Text>
                            </View>
                        </View>
                        <Ionicons name="chevron-forward" size={20} color="#9CA3AF" />
                    </TouchableOpacity>
                </View>

                <TouchableOpacity
                    onPress={handleSell}
                    disabled={!amount}
                    style={[
                        styles.confirmButton,
                        amount ? styles.confirmButtonActive : styles.confirmButtonDisabled
                    ]}
                >
                    <Text style={styles.confirmButtonText}>Confirm Sale</Text>
                </TouchableOpacity>

                <Text style={styles.disclaimer}>
                    By confirming, you agree to the transaction terms and current market rates.
                </Text>
            </ScrollView>
        </SafeAreaView>
    );
};

const styles = StyleSheet.create({
    container: {
        flex: 1,
        backgroundColor: 'white',
    },
    header: {
        flexDirection: 'row',
        alignItems: 'center',
        paddingHorizontal: 16,
        paddingVertical: 16,
        borderBottomWidth: 1,
        borderBottomColor: '#F3F4F6',
    },
    backButton: {
        padding: 4,
    },
    headerTitle: {
        fontSize: 20,
        fontWeight: 'bold',
        marginLeft: 16,
        color: '#111827',
    },
    content: {
        flex: 1,
    },
    scrollContent: {
        paddingHorizontal: 20,
        paddingTop: 24,
        paddingBottom: 40,
    },
    section: {
        marginBottom: 24,
    },
    label: {
        fontSize: 14,
        fontWeight: '500',
        color: '#6B7280',
        marginBottom: 8,
    },
    inputContainer: {
        flexDirection: 'row',
        alignItems: 'center',
        backgroundColor: '#F9FAFB',
        borderWidth: 1,
        borderColor: '#E5E7EB',
        borderRadius: 16,
        paddingHorizontal: 16,
        paddingVertical: 16,
    },
    input: {
        flex: 1,
        fontSize: 24,
        fontWeight: 'bold',
        color: '#111827',
    },
    currencyBadge: {
        flexDirection: 'row',
        alignItems: 'center',
        backgroundColor: 'white',
        paddingHorizontal: 12,
        paddingVertical: 4,
        borderRadius: 9999,
        borderWidth: 1,
        borderColor: '#F3F4F6',
    },
    currencyText: {
        fontWeight: 'bold',
        color: '#374151',
    },
    limitContainer: {
        flexDirection: 'row',
        justifyContent: 'space-between',
        marginTop: 8,
    },
    balanceText: {
        fontSize: 12,
        color: '#9CA3AF',
    },
    sellMaxText: {
        fontSize: 12,
        color: '#2563EB',
        fontWeight: 'bold',
    },
    receiveContainer: {
        backgroundColor: '#EFF6FF',
        borderRadius: 16,
        padding: 20,
        borderWidth: 1,
        borderColor: '#DBEAFE',
    },
    receiveAmount: {
        fontSize: 30,
        fontWeight: '800',
        color: '#1D4ED8',
    },
    rateContainer: {
        flexDirection: 'row',
        alignItems: 'center',
        marginTop: 8,
    },
    rateText: {
        fontSize: 12,
        color: '#2563EB',
        marginLeft: 4,
        fontWeight: '500',
    },
    payoutMethodContainer: {
        flexDirection: 'row',
        alignItems: 'center',
        justifyContent: 'space-between',
        backgroundColor: 'white',
        borderWidth: 1,
        borderColor: '#E5E7EB',
        borderRadius: 16,
        paddingHorizontal: 16,
        paddingVertical: 16,
    },
    payoutLeft: {
        flexDirection: 'row',
        alignItems: 'center',
    },
    payoutIconContainer: {
        width: 40,
        height: 40,
        backgroundColor: '#F3F4F6',
        borderRadius: 9999,
        alignItems: 'center',
        justifyContent: 'center',
    },
    payoutDetails: {
        marginLeft: 12,
    },
    payoutTitle: {
        fontWeight: 'bold',
        color: '#111827',
    },
    payoutSubtitle: {
        fontSize: 12,
        color: '#6B7280',
    },
    confirmButton: {
        paddingVertical: 16,
        borderRadius: 16,
        alignItems: 'center',
        shadowColor: '#000',
        shadowOffset: {
            width: 0,
            height: 1,
        },
        shadowOpacity: 0.2,
        shadowRadius: 1.41,
        elevation: 2,
    },
    confirmButtonActive: {
        backgroundColor: '#2563EB',
    },
    confirmButtonDisabled: {
        backgroundColor: '#D1D5DB',
    },
    confirmButtonText: {
        color: 'white',
        fontWeight: 'bold',
        fontSize: 18,
    },
    disclaimer: {
        textAlign: 'center',
        color: '#9CA3AF',
        fontSize: 12,
        marginTop: 16,
        paddingHorizontal: 24,
    },
});

export default SellUSDTScreen;
