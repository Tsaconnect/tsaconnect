
import React from 'react';
import { View, Text, StyleSheet, FlatList, TouchableOpacity } from 'react-native';
import { SafeAreaView } from "react-native-safe-area-context"
const stats = [
  { id: '1', label: 'Total Revenue', value: '$12,840.00', color: '#4CAF50' },
  { id: '2', label: 'Active Orders', value: '42', color: '#2196F3' },
  { id: '3', label: 'Pending Payout', value: '$1,250.00', color: '#FF9800' },
];

const transactions = [
  { id: '1', customer: 'Alice Smith', amount: '$120.00', status: 'Completed', time: '2h ago' },
  { id: '2', customer: 'Bob Johnson', amount: '$45.50', status: 'Pending', time: '4h ago' },
  { id: '3', customer: 'Charlie Brown', amount: '$210.00', status: 'Completed', time: '1d ago' },
];

export default function MerchantDashboard() {
  return (
    <SafeAreaView style={styles.container}>
      <View style={styles.header}>
        <Text style={styles.title}>Dashboard</Text>
        <Text style={styles.subtitle}>Welcome back, Merchant</Text>
      </View>

      <View style={styles.statsGrid}>
        {stats.map((stat) => (
          <View key={stat.id} style={[styles.statCard, { borderLeftColor: stat.color }]}>
            <Text style={styles.statLabel}>{stat.label}</Text>
            <Text style={styles.statValue}>{stat.value}</Text>
          </View>
        ))}
      </View>

      <View style={styles.section}>
        <Text style={styles.sectionTitle}>Recent Transactions</Text>
        <FlatList
          data={transactions}
          keyExtractor={(item) => item.id}
          renderItem={({ item }) => (
            <TouchableOpacity style={styles.transactionItem}>
              <View>
                <Text style={styles.customerName}>{item.customer}</Text>
                <Text style={styles.transactionTime}>{item.time}</Text>
              </View>
              <View style={styles.transactionRight}>
                <Text style={styles.amount}>{item.amount}</Text>
                <Text style={[styles.status, { color: item.status === 'Completed' ? '#4CAF50' : '#FF9800' }]}>
                  {item.status}
                </Text>
              </View>
            </TouchableOpacity>
          )}
        />
      </View>
    </SafeAreaView>
  );
}

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#F9FAFB' },
  header: { padding: 20, backgroundColor: '#FFFFFF' },
  title: { fontSize: 28, fontWeight: 'bold', color: '#111827' },
  subtitle: { fontSize: 16, color: '#6B7280', marginTop: 4 },
  statsGrid: { flexDirection: 'row', flexWrap: 'wrap', padding: 10, justifyContent: 'space-between' },
  statCard: {
    backgroundColor: '#FFFFFF',
    width: '48%',
    padding: 16,
    borderRadius: 12,
    marginBottom: 12,
    borderLeftWidth: 4,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.05,
    shadowRadius: 4,
    elevation: 2,
  },
  statLabel: { fontSize: 12, color: '#6B7280', marginBottom: 4 },
  statValue: { fontSize: 18, fontWeight: 'bold', color: '#111827' },
  section: { flex: 1, paddingHorizontal: 20, marginTop: 10 },
  sectionTitle: { fontSize: 18, fontWeight: '600', color: '#111827', marginBottom: 16 },
  transactionItem: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    backgroundColor: '#FFFFFF',
    padding: 16,
    borderRadius: 12,
    marginBottom: 10,
    alignItems: 'center',
  },
  customerName: { fontSize: 16, fontWeight: '500', color: '#111827' },
  transactionTime: { fontSize: 13, color: '#9CA3AF', marginTop: 2 },
  transactionRight: { alignItems: 'flex-end' },
  amount: { fontSize: 16, fontWeight: 'bold', color: '#111827' },
  status: { fontSize: 12, fontWeight: '600', marginTop: 4 },
});
