import React, { useEffect, useState, useCallback } from 'react';
import {
  View,
  Text,
  StyleSheet,
  ScrollView,
  TouchableOpacity,
  ActivityIndicator,
  Alert,
  Pressable,
} from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import { router, useLocalSearchParams } from 'expo-router';
import { SafeAreaView } from 'react-native-safe-area-context';
import { LinearGradient } from 'expo-linear-gradient';
import * as Clipboard from 'expo-clipboard';
import {
  getOrderById,
  prepareConfirm,
  submitConfirm,
  requestRefund,
  formatTokenAmount,
  Order,
} from '@/services/orderApi';
import { signAndBroadcast } from '@/services/transaction';
import { STATUS_COLORS, formatStatus, formatDate } from '@/constants/orderStatus';

const truncateHash = (hash?: string): string => {
  if (!hash) return '—';
  if (hash.length <= 16) return hash;
  return `${hash.slice(0, 10)}...${hash.slice(-6)}`;
};

const OrderDetailScreen = () => {
  const { id } = useLocalSearchParams<{ id: string }>();
  const [order, setOrder] = useState<Order | null>(null);
  const [loading, setLoading] = useState(true);
  const [actionLoading, setActionLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const fetchOrder = useCallback(async () => {
    if (!id) return;
    setLoading(true);
    setError(null);
    const result = await getOrderById(id);
    if (result.success && result.data) {
      setOrder(result.data);
    } else {
      setError(result.message || 'Failed to load order');
    }
    setLoading(false);
  }, [id]);

  useEffect(() => {
    fetchOrder();
  }, [fetchOrder]);

  const handleConfirmReceipt = () => {
    Alert.alert(
      'Confirm Receipt',
      'This will release funds to the seller. Are you sure you received the item?',
      [
        { text: 'Cancel', style: 'cancel' },
        {
          text: 'Confirm',
          onPress: async () => {
            if (!order) return;
            setActionLoading(true);
            try {
              const prepResult = await prepareConfirm(order.id);
              if (!prepResult.success || !prepResult.data) {
                throw new Error(prepResult.message || 'Failed to prepare confirm');
              }

              const txHash = await signAndBroadcast(prepResult.data.confirmTx);

              const submitResult = await submitConfirm(order.id, txHash);
              if (!submitResult.success) {
                throw new Error(submitResult.message || 'Failed to submit confirm');
              }

              Alert.alert('Success', 'Receipt confirmed! Funds released to seller.');
              fetchOrder();
            } catch (err: any) {
              Alert.alert('Error', err.message || 'Failed to confirm receipt');
            } finally {
              setActionLoading(false);
            }
          },
        },
      ]
    );
  };

  const handleRequestRefund = () => {
    Alert.alert(
      'Request Refund',
      order?.status === 'escrowed'
        ? 'This will initiate an on-chain refund request.'
        : 'This will send your refund request for admin review.',
      [
        { text: 'Cancel', style: 'cancel' },
        {
          text: 'Request Refund',
          style: 'destructive',
          onPress: async () => {
            if (!order) return;
            setActionLoading(true);
            try {
              const result = await requestRefund(order.id);
              if (!result.success) {
                throw new Error(result.message || 'Failed to request refund');
              }

              // If escrowed, we get a refundTx to sign and broadcast
              if (order.status === 'escrowed' && result.data?.refundTx) {
                const txHash = await signAndBroadcast(result.data.refundTx);
                Alert.alert(
                  'Refund Requested',
                  `On-chain refund request submitted.\nTx: ${txHash.slice(0, 16)}...`,
                );
              } else {
                Alert.alert('Success', 'Refund request submitted for review.');
              }
              fetchOrder();
            } catch (err: any) {
              Alert.alert('Error', err.message || 'Failed to request refund');
            } finally {
              setActionLoading(false);
            }
          },
        },
      ]
    );
  };

  const copyToClipboard = async (text: string) => {
    await Clipboard.setStringAsync(text);
    Alert.alert('Copied', 'Copied to clipboard');
  };

  if (loading) {
    return (
      <SafeAreaView style={styles.container}>
        <LinearGradient colors={['#FDF8F3', '#FAF0E6']} style={styles.gradientBackground}>
          <View style={styles.header}>
            <Pressable onPress={() => router.back()} style={styles.backButton}>
              <Ionicons name="chevron-back" size={24} color="#8B5A2B" />
            </Pressable>
            <Text style={styles.headerTitle}>Order Detail</Text>
            <View style={{ width: 24 }} />
          </View>
          <View style={styles.centered}>
            <ActivityIndicator size="large" color="#8B5A2B" />
          </View>
        </LinearGradient>
      </SafeAreaView>
    );
  }

  if (error || !order) {
    return (
      <SafeAreaView style={styles.container}>
        <LinearGradient colors={['#FDF8F3', '#FAF0E6']} style={styles.gradientBackground}>
          <View style={styles.header}>
            <Pressable onPress={() => router.back()} style={styles.backButton}>
              <Ionicons name="chevron-back" size={24} color="#8B5A2B" />
            </Pressable>
            <Text style={styles.headerTitle}>Order Detail</Text>
            <View style={{ width: 24 }} />
          </View>
          <View style={styles.centered}>
            <Ionicons name="alert-circle-outline" size={48} color="#8B5A2B" />
            <Text style={styles.errorText}>{error || 'Order not found'}</Text>
            <TouchableOpacity onPress={fetchOrder} style={styles.retryButton}>
              <Text style={styles.retryText}>Try Again</Text>
            </TouchableOpacity>
          </View>
        </LinearGradient>
      </SafeAreaView>
    );
  }

  const statusColor = STATUS_COLORS[order.status] || STATUS_COLORS.cancelled;

  return (
    <SafeAreaView style={styles.container}>
      <LinearGradient colors={['#FDF8F3', '#FAF0E6']} style={styles.gradientBackground}>
        <View style={styles.header}>
          <Pressable onPress={() => router.back()} style={styles.backButton}>
            <Ionicons name="chevron-back" size={24} color="#8B5A2B" />
          </Pressable>
          <Text style={styles.headerTitle}>Order Detail</Text>
          <View style={{ width: 24 }} />
        </View>

        <ScrollView showsVerticalScrollIndicator={false} contentContainerStyle={styles.scrollContent}>
          {/* Status badge */}
          <View style={styles.statusContainer}>
            <View style={[styles.statusBadgeLarge, { backgroundColor: statusColor.bg }]}>
              <Text style={[styles.statusTextLarge, { color: statusColor.text }]}>
                {formatStatus(order.status)}
              </Text>
            </View>
            {order.status === 'completed' && (
              <Ionicons name="checkmark-circle" size={32} color="#065F46" style={{ marginTop: 8 }} />
            )}
          </View>

          {/* Order info */}
          <View style={styles.card}>
            <Text style={styles.cardTitle}>Order Info</Text>
            <InfoRow label="Order ID" value={order.id.slice(0, 16) + '...'} onCopy={() => copyToClipboard(order.id)} />
            <InfoRow label="Quantity" value={String(order.quantity)} />
            <InfoRow label="Token" value={order.token} />
            <InfoRow label="Shipping Zone" value={formatStatus(order.shippingZone || 'N/A')} />
          </View>

          {/* Amounts */}
          <View style={styles.card}>
            <Text style={styles.cardTitle}>Amounts</Text>
            <InfoRow label="Product" value={formatTokenAmount(order.productAmount, order.token)} />
            <InfoRow label="Shipping" value={formatTokenAmount(order.shippingAmount, order.token)} />
            <InfoRow label="Platform Fee" value={formatTokenAmount(order.platformFee, order.token)} />
            <View style={styles.divider} />
            <InfoRow
              label="Total"
              value={formatTokenAmount(order.totalAmount, order.token)}
              bold
            />
          </View>

          {/* Blockchain info */}
          {(order.contractOrderId || order.escrowTxHash || order.approveTxHash || order.releaseTxHash) && (
            <View style={styles.card}>
              <Text style={styles.cardTitle}>Blockchain</Text>
              {order.contractOrderId && (
                <InfoRow
                  label="Contract Order"
                  value={truncateHash(order.contractOrderId)}
                  onCopy={() => copyToClipboard(order.contractOrderId!)}
                />
              )}
              {order.approveTxHash && (
                <InfoRow
                  label="Approve Tx"
                  value={truncateHash(order.approveTxHash)}
                  onCopy={() => copyToClipboard(order.approveTxHash!)}
                />
              )}
              {order.escrowTxHash && (
                <InfoRow
                  label="Escrow Tx"
                  value={truncateHash(order.escrowTxHash)}
                  onCopy={() => copyToClipboard(order.escrowTxHash!)}
                />
              )}
              {order.releaseTxHash && (
                <InfoRow
                  label="Release Tx"
                  value={truncateHash(order.releaseTxHash)}
                  onCopy={() => copyToClipboard(order.releaseTxHash!)}
                />
              )}
            </View>
          )}

          {/* Timeline */}
          <View style={styles.card}>
            <Text style={styles.cardTitle}>Timeline</Text>
            <InfoRow label="Created" value={formatDate(order.createdAt, true)} />
            {order.sellerDeliveredAt && (
              <InfoRow label="Delivered" value={formatDate(order.sellerDeliveredAt, true)} />
            )}
            {order.buyerConfirmedAt && (
              <InfoRow label="Confirmed" value={formatDate(order.buyerConfirmedAt, true)} />
            )}
            {order.escrowExpiresAt && (
              <InfoRow label="Escrow Expires" value={formatDate(order.escrowExpiresAt, true)} />
            )}
          </View>

          {/* Action buttons */}
          {actionLoading ? (
            <View style={styles.actionLoadingContainer}>
              <ActivityIndicator size="large" color="#8B5A2B" />
              <Text style={styles.actionLoadingText}>Processing transaction...</Text>
            </View>
          ) : (
            <>
              {order.status === 'escrowed' && (
                <View style={styles.actionContainer}>
                  <View style={styles.waitingBanner}>
                    <Ionicons name="time-outline" size={20} color="#0C5460" />
                    <Text style={styles.waitingText}>Waiting for seller to deliver</Text>
                  </View>
                  <TouchableOpacity style={styles.refundButton} onPress={handleRequestRefund}>
                    <Text style={styles.refundButtonText}>Request Refund</Text>
                  </TouchableOpacity>
                </View>
              )}

              {order.status === 'delivered' && (
                <View style={styles.actionContainer}>
                  <TouchableOpacity style={styles.confirmButton} onPress={handleConfirmReceipt}>
                    <LinearGradient
                      colors={['#2E7D32', '#1B5E20']}
                      start={{ x: 0, y: 0 }}
                      end={{ x: 1, y: 0 }}
                      style={styles.actionGradient}
                    >
                      <Ionicons name="checkmark-circle-outline" size={20} color="#FFF" />
                      <Text style={styles.confirmButtonText}>Confirm Receipt</Text>
                    </LinearGradient>
                  </TouchableOpacity>
                  <TouchableOpacity style={styles.refundButton} onPress={handleRequestRefund}>
                    <Text style={styles.refundButtonText}>Request Refund</Text>
                  </TouchableOpacity>
                </View>
              )}

              {order.status === 'pending_payment' && (
                <View style={styles.actionContainer}>
                  <View style={styles.waitingBanner}>
                    <Ionicons name="wallet-outline" size={20} color="#856404" />
                    <Text style={[styles.waitingText, { color: '#856404' }]}>
                      Awaiting payment. Complete checkout to fund escrow.
                    </Text>
                  </View>
                </View>
              )}
            </>
          )}

          <View style={{ height: 32 }} />
        </ScrollView>
      </LinearGradient>
    </SafeAreaView>
  );
};

// Reusable info row
const InfoRow = ({
  label,
  value,
  bold,
  onCopy,
}: {
  label: string;
  value: string;
  bold?: boolean;
  onCopy?: () => void;
}) => (
  <View style={infoRowStyles.row}>
    <Text style={infoRowStyles.label}>{label}</Text>
    <View style={infoRowStyles.valueContainer}>
      <Text style={[infoRowStyles.value, bold && infoRowStyles.boldValue]}>{value}</Text>
      {onCopy && (
        <TouchableOpacity onPress={onCopy} style={infoRowStyles.copyButton}>
          <Ionicons name="copy-outline" size={14} color="#8B5A2B" />
        </TouchableOpacity>
      )}
    </View>
  </View>
);

const infoRowStyles = StyleSheet.create({
  row: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    paddingVertical: 8,
  },
  label: {
    fontSize: 13,
    color: '#A67C52',
    flex: 1,
  },
  valueContainer: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 6,
    flex: 1,
    justifyContent: 'flex-end',
  },
  value: {
    fontSize: 13,
    color: '#4A2C1A',
    fontWeight: '500',
    textAlign: 'right',
  },
  boldValue: {
    fontSize: 15,
    fontWeight: '700',
    color: '#8B5A2B',
  },
  copyButton: {
    padding: 4,
  },
});

const styles = StyleSheet.create({
  container: {
    flex: 1,
  },
  gradientBackground: {
    flex: 1,
  },
  header: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    paddingHorizontal: 20,
    paddingVertical: 16,
  },
  backButton: {
    width: 40,
    height: 40,
    borderRadius: 20,
    backgroundColor: '#FFF',
    justifyContent: 'center',
    alignItems: 'center',
    shadowColor: '#8B5A2B',
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.1,
    shadowRadius: 4,
    elevation: 2,
  },
  headerTitle: {
    fontSize: 20,
    fontWeight: '700',
    color: '#4A2C1A',
  },
  scrollContent: {
    padding: 20,
    paddingTop: 8,
  },
  centered: {
    flex: 1,
    justifyContent: 'center',
    alignItems: 'center',
    gap: 12,
  },
  errorText: {
    color: '#4A2C1A',
    fontSize: 16,
    textAlign: 'center',
    paddingHorizontal: 32,
  },
  retryButton: {
    marginTop: 8,
    paddingHorizontal: 24,
    paddingVertical: 10,
    backgroundColor: '#8B5A2B',
    borderRadius: 12,
  },
  retryText: {
    color: '#FFF',
    fontWeight: '600',
  },
  // Status
  statusContainer: {
    alignItems: 'center',
    marginBottom: 20,
  },
  statusBadgeLarge: {
    paddingHorizontal: 20,
    paddingVertical: 10,
    borderRadius: 20,
  },
  statusTextLarge: {
    fontSize: 16,
    fontWeight: '700',
  },
  // Cards
  card: {
    backgroundColor: '#FFF',
    padding: 16,
    borderRadius: 16,
    borderWidth: 1,
    borderColor: '#E8D5C0',
    marginBottom: 16,
    shadowColor: '#8B5A2B',
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.05,
    shadowRadius: 4,
    elevation: 2,
  },
  cardTitle: {
    fontSize: 15,
    fontWeight: '600',
    color: '#4A2C1A',
    marginBottom: 8,
  },
  divider: {
    height: 1,
    backgroundColor: '#E8D5C0',
    marginVertical: 8,
  },
  // Actions
  actionContainer: {
    marginTop: 8,
    gap: 12,
  },
  confirmButton: {
    borderRadius: 16,
    overflow: 'hidden',
  },
  actionGradient: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    paddingVertical: 16,
    paddingHorizontal: 24,
    gap: 8,
    borderRadius: 16,
  },
  confirmButtonText: {
    color: '#FFF',
    fontSize: 16,
    fontWeight: '700',
  },
  refundButton: {
    borderRadius: 16,
    borderWidth: 1.5,
    borderColor: '#EF4444',
    paddingVertical: 14,
    alignItems: 'center',
  },
  refundButtonText: {
    color: '#EF4444',
    fontSize: 15,
    fontWeight: '600',
  },
  waitingBanner: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 8,
    backgroundColor: '#D1ECF1',
    padding: 14,
    borderRadius: 12,
  },
  waitingText: {
    fontSize: 13,
    color: '#0C5460',
    flex: 1,
  },
  actionLoadingContainer: {
    alignItems: 'center',
    paddingVertical: 24,
    gap: 12,
  },
  actionLoadingText: {
    fontSize: 14,
    color: '#8B5A2B',
  },
});

export default OrderDetailScreen;
