import React, { useState, useEffect, useCallback } from 'react';
import { View, Text, TouchableOpacity, StyleSheet } from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import { useRouter } from 'expo-router';
import { useAuth } from '@/AuthContext/AuthContext';
import { cartService } from '@/components/services/cart';

interface CartIconBadgeProps {
  color?: string;
  size?: number;
}

export function useCartCount() {
  const [cartCount, setCartCount] = useState(0);
  const { token } = useAuth();

  const refresh = useCallback(async () => {
    if (!token) {
      setCartCount(0);
      return;
    }
    try {
      cartService.setToken(token);
      const count = await cartService.getCartItemCount();
      setCartCount(count);
    } catch {
      // silent
    }
  }, [token]);

  useEffect(() => {
    refresh();
  }, [refresh]);

  // Refresh on a short interval so badge stays current across screens
  useEffect(() => {
    if (!token) return;
    const interval = setInterval(refresh, 10000);
    return () => clearInterval(interval);
  }, [token, refresh]);

  return { cartCount, refreshCartCount: refresh };
}

export default function CartIconBadge({
  color = '#D4AF37',
  size = 24,
}: CartIconBadgeProps) {
  const router = useRouter();
  const { cartCount } = useCartCount();

  return (
    <TouchableOpacity
      style={styles.container}
      onPress={() => router.push('/cart')}
      activeOpacity={0.7}
    >
      <Ionicons name="cart-outline" size={size} color={color} />
      {cartCount > 0 && (
        <View style={styles.badge}>
          <Text style={styles.badgeText}>
            {cartCount > 99 ? '99+' : cartCount}
          </Text>
        </View>
      )}
    </TouchableOpacity>
  );
}

const styles = StyleSheet.create({
  container: {
    padding: 6,
    position: 'relative',
  },
  badge: {
    position: 'absolute',
    top: 0,
    right: 0,
    backgroundColor: '#DC2626',
    borderRadius: 10,
    minWidth: 18,
    height: 18,
    justifyContent: 'center',
    alignItems: 'center',
    paddingHorizontal: 4,
    borderWidth: 1.5,
    borderColor: '#FFF',
  },
  badgeText: {
    color: '#FFF',
    fontSize: 10,
    fontWeight: '700',
    lineHeight: 12,
  },
});
