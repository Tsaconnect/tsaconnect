import React, { useState, useEffect, useCallback } from 'react';
import {
  View,
  Text,
  StyleSheet,
  FlatList,
  TouchableOpacity,
  ActivityIndicator,
  RefreshControl,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router, useFocusEffect } from 'expo-router';
import { Ionicons } from '@expo/vector-icons';
import api from '../../../components/services/api';
import AsyncStorage from '@react-native-async-storage/async-storage';

export default function MerchantDashboard() {
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [stats, setStats] = useState<any>(null);
  const [products, setProducts] = useState<any[]>([]);
  const [merchantName, setMerchantName] = useState('Merchant');

  const loadDashboard = async () => {
    try {
      const token = await AsyncStorage.getItem('token');
      if (token) {
        api.setToken(token.replace('Bearer ', ''));
      }

      const [statsRes, productsRes] = await Promise.all([
        api.getProductStats(),
        api.getMerchantProducts(),
      ]);

      if (statsRes.success && statsRes.data) {
        setStats(statsRes.data);
      }

      if (productsRes.success && productsRes.data) {
        const data = productsRes.data;
        setProducts(Array.isArray(data) ? data : data.products || []);
      }

      const name = await AsyncStorage.getItem('username');
      if (name) setMerchantName(name);
    } catch (error) {
      console.error('Dashboard load error:', error);
    } finally {
      setLoading(false);
      setRefreshing(false);
    }
  };

  useFocusEffect(
    useCallback(() => {
      loadDashboard();
    }, [])
  );

  const onRefresh = () => {
    setRefreshing(true);
    loadDashboard();
  };

  if (loading) {
    return (
      <SafeAreaView style={styles.center}>
        <ActivityIndicator size="large" color="#9b795f" />
      </SafeAreaView>
    );
  }

  const totalProducts = stats?.totalProducts ?? products.length;
  const activeProducts = stats?.activeProducts ?? products.filter((p: any) => p.status === 'active').length;
  const outOfStock = stats?.outOfStock ?? products.filter((p: any) => p.stock === 0).length;

  return (
    <SafeAreaView style={styles.container}>
      <FlatList
        data={products.slice(0, 10)}
        keyExtractor={(item) => item.id || item._id}
        refreshControl={
          <RefreshControl refreshing={refreshing} onRefresh={onRefresh} tintColor="#9b795f" />
        }
        ListHeaderComponent={
          <>
            <View style={styles.header}>
              <Text style={styles.title}>Dashboard</Text>
              <Text style={styles.subtitle}>Welcome back, {merchantName}</Text>
            </View>

            <View style={styles.statsGrid}>
              <View style={[styles.statCard, { borderLeftColor: '#4CAF50' }]}>
                <Text style={styles.statLabel}>Total Products</Text>
                <Text style={styles.statValue}>{totalProducts}</Text>
              </View>
              <View style={[styles.statCard, { borderLeftColor: '#2196F3' }]}>
                <Text style={styles.statLabel}>Active Products</Text>
                <Text style={styles.statValue}>{activeProducts}</Text>
              </View>
              <View style={[styles.statCard, { borderLeftColor: '#FF9800' }]}>
                <Text style={styles.statLabel}>Out of Stock</Text>
                <Text style={styles.statValue}>{outOfStock}</Text>
              </View>
            </View>

            <View style={styles.quickActions}>
              <TouchableOpacity
                style={styles.actionButton}
                onPress={() => router.push('/merchants/inventory/add')}
              >
                <Ionicons name="add-circle-outline" size={24} color="#fff" />
                <Text style={styles.actionButtonText}>Add Product</Text>
              </TouchableOpacity>
              <TouchableOpacity
                style={[styles.actionButton, { backgroundColor: '#2196F3' }]}
                onPress={() => router.push('/merchants/inventory')}
              >
                <Ionicons name="list-outline" size={24} color="#fff" />
                <Text style={styles.actionButtonText}>View Inventory</Text>
              </TouchableOpacity>
            </View>

            <Text style={styles.sectionTitle}>
              {products.length > 0 ? 'Recent Products' : 'No Products Yet'}
            </Text>

            {products.length === 0 && (
              <View style={styles.emptyState}>
                <Ionicons name="cube-outline" size={64} color="#ccc" />
                <Text style={styles.emptyText}>You haven't added any products yet.</Text>
                <TouchableOpacity
                  style={styles.emptyButton}
                  onPress={() => router.push('/merchants/inventory/add')}
                >
                  <Text style={styles.emptyButtonText}>Add Your First Product</Text>
                </TouchableOpacity>
              </View>
            )}
          </>
        }
        renderItem={({ item }) => (
          <TouchableOpacity
            style={styles.productItem}
            activeOpacity={0.7}
            onPress={() => router.push({
              pathname: '/merchants/inventory/edit/[productId]',
              params: { productId: item.id || item._id, productData: JSON.stringify(item) },
            })}
          >
            <View style={{ flex: 1 }}>
              <Text style={styles.productName}>{item.name}</Text>
              <Text style={styles.productMeta}>
                Stock: {item.stock ?? 'N/A'} | {item.status || 'draft'}
              </Text>
            </View>
            <Text style={styles.productPrice}>
              ${item.price != null ? Number(item.price).toFixed(2) : '0.00'}
            </Text>
          </TouchableOpacity>
        )}
        ListEmptyComponent={null}
      />
    </SafeAreaView>
  );
}

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#F9FAFB' },
  center: { flex: 1, justifyContent: 'center', alignItems: 'center', backgroundColor: '#F9FAFB' },
  header: { padding: 20, backgroundColor: '#FFFFFF' },
  title: { fontSize: 28, fontWeight: 'bold', color: '#111827' },
  subtitle: { fontSize: 16, color: '#6B7280', marginTop: 4 },
  statsGrid: { flexDirection: 'row', flexWrap: 'wrap', padding: 10, gap: 10 },
  statCard: {
    backgroundColor: '#FFFFFF',
    flex: 1,
    minWidth: '30%',
    padding: 16,
    borderRadius: 12,
    borderLeftWidth: 4,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.05,
    shadowRadius: 4,
    elevation: 2,
  },
  statLabel: { fontSize: 12, color: '#6B7280', marginBottom: 4 },
  statValue: { fontSize: 20, fontWeight: 'bold', color: '#111827' },
  quickActions: {
    flexDirection: 'row',
    paddingHorizontal: 10,
    gap: 10,
    marginVertical: 10,
  },
  actionButton: {
    flex: 1,
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: '#9b795f',
    padding: 14,
    borderRadius: 12,
    gap: 8,
  },
  actionButtonText: { color: '#fff', fontSize: 14, fontWeight: '600' },
  sectionTitle: {
    fontSize: 18,
    fontWeight: '600',
    color: '#111827',
    paddingHorizontal: 20,
    marginTop: 10,
    marginBottom: 12,
  },
  productItem: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    backgroundColor: '#FFFFFF',
    padding: 16,
    marginHorizontal: 10,
    borderRadius: 12,
    marginBottom: 8,
  },
  productName: { fontSize: 15, fontWeight: '500', color: '#111827' },
  productMeta: { fontSize: 12, color: '#9CA3AF', marginTop: 2 },
  productPrice: { fontSize: 16, fontWeight: 'bold', color: '#111827' },
  emptyState: { alignItems: 'center', padding: 40 },
  emptyText: { fontSize: 15, color: '#9CA3AF', marginTop: 12, marginBottom: 16 },
  emptyButton: {
    backgroundColor: '#9b795f',
    paddingHorizontal: 24,
    paddingVertical: 12,
    borderRadius: 8,
  },
  emptyButtonText: { color: '#fff', fontSize: 14, fontWeight: '600' },
});
