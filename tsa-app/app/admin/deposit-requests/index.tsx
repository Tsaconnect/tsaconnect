import React, { useState, useCallback } from 'react';
import {
  View,
  Text,
  FlatList,
  TouchableOpacity,
  StyleSheet,
  RefreshControl,
  ActivityIndicator,
} from 'react-native';
import { SafeAreaView } from "react-native-safe-area-context"
interface DepositRequest {
  id: string;
  userEmail: string;
  amount: number;
  currency: string;
  status: 'pending' | 'approved' | 'rejected';
  timestamp: string;
}

const DepositRequestsScreen = () => {
  const [requests, setRequests] = useState<DepositRequest[]>([]);
  const [loading, setLoading] = useState(false);
  const [refreshing, setRefreshing] = useState(false);

  const fetchRequests = useCallback(async () => {
    setLoading(true);
    try {
      // API call logic would go here
    } finally {
      setLoading(false);
      setRefreshing(false);
    }
  }, []);

  const handleAction = (id: string, action: 'approve' | 'reject') => {
    // Handle status update logic
  };

  const renderItem = ({ item }: { item: DepositRequest }) => (
    <View style={styles.card}>
      <View style={styles.cardHeader}>
        <Text style={styles.userEmail}>{item.userEmail}</Text>
        <View style={[styles.badge, styles[item.status]]}>
          <Text style={styles.badgeText}>{item.status.toUpperCase()}</Text>
        </View>
      </View>
      
      <View style={styles.amountContainer}>
        <Text style={styles.amountLabel}>Requested Amount</Text>
        <Text style={styles.amountValue}>{item.amount} {item.currency}</Text>
      </View>

      <Text style={styles.timestamp}>{new Date(item.timestamp).toLocaleString()}</Text>

      {item.status === 'pending' && (
        <View style={styles.actionContainer}>
          <TouchableOpacity 
            style={[styles.button, styles.approveButton]} 
            onPress={() => handleAction(item.id, 'approve')}
          >
            <Text style={styles.buttonText}>Approve</Text>
          </TouchableOpacity>
          <TouchableOpacity 
            style={[styles.button, styles.rejectButton]} 
            onPress={() => handleAction(item.id, 'reject')}
          >
            <Text style={styles.buttonText}>Reject</Text>
          </TouchableOpacity>
        </View>
      )}
    </View>
  );

  return (
    <SafeAreaView style={styles.container}>
      <View style={styles.header}>
        <Text style={styles.title}>Deposit Requests</Text>
      </View>

      {loading && !refreshing ? (
        <ActivityIndicator size="large" color="#007AFF" style={styles.loader} />
      ) : (
        <FlatList
          data={requests}
          renderItem={renderItem}
          keyExtractor={(item) => item.id}
          contentContainerStyle={styles.listContent}
          refreshControl={
            <RefreshControl refreshing={refreshing} onRefresh={() => { setRefreshing(true); fetchRequests(); }} />
          }
          ListEmptyComponent={
            <Text style={styles.emptyText}>No pending deposit requests found.</Text>
          }
        />
      )}
    </SafeAreaView>
  );
};

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#F8F9FA' },
  header: { padding: 20, backgroundColor: '#FFF', borderBottomWidth: 1, borderBottomColor: '#EEE' },
  title: { fontSize: 22, fontWeight: '700', color: '#1A1A1A' },
  listContent: { padding: 16 },
  card: { backgroundColor: '#FFF', borderRadius: 12, padding: 16, marginBottom: 16, elevation: 2, shadowColor: '#000', shadowOffset: { width: 0, height: 2 }, shadowOpacity: 0.1, shadowRadius: 4 },
  cardHeader: { flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', marginBottom: 12 },
  userEmail: { fontSize: 14, fontWeight: '600', color: '#444' },
  badge: { paddingHorizontal: 8, paddingVertical: 4, borderRadius: 6 },
  badgeText: { fontSize: 10, fontWeight: 'bold', color: '#FFF' },
  pending: { backgroundColor: '#F59E0B' },
  approved: { backgroundColor: '#10B981' },
  rejected: { backgroundColor: '#EF4444' },
  amountContainer: { marginBottom: 8 },
  amountLabel: { fontSize: 12, color: '#888' },
  amountValue: { fontSize: 20, fontWeight: 'bold', color: '#111' },
  timestamp: { fontSize: 12, color: '#AAA' },
  actionContainer: { flexDirection: 'row', gap: 10, marginTop: 16 },
  button: { flex: 1, paddingVertical: 10, borderRadius: 8, alignItems: 'center' },
  approveButton: { backgroundColor: '#10B981' },
  rejectButton: { backgroundColor: '#EF4444' },
  buttonText: { color: '#FFF', fontWeight: '600' },
  loader: { flex: 1, justifyContent: 'center' },
  emptyText: { textAlign: 'center', marginTop: 40, color: '#999', fontSize: 16 },
});

export default DepositRequestsScreen;
