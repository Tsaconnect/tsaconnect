import React from 'react';
import { View, Text, StyleSheet, TouchableOpacity } from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import { router } from 'expo-router';

const GOLD = '#D4AF37';

export default function FundFiatScreen() {
  return (
    <View style={s.container}>
      <View style={s.center}>
        <View style={s.iconWrap}>
          <Ionicons name="time-outline" size={48} color={GOLD} />
        </View>
        <Text style={s.title}>Coming Soon</Text>
        <Text style={s.subtitle}>
          Fiat deposits are on the way. You'll be able to fund your wallet via bank transfer and other local payment methods.
        </Text>
        <TouchableOpacity style={s.btn} onPress={() => router.back()}>
          <Text style={s.btnText}>Go Back</Text>
        </TouchableOpacity>
      </View>
    </View>
  );
}

const s = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#FAFAFA' },
  center: { flex: 1, alignItems: 'center', justifyContent: 'center', paddingHorizontal: 40 },
  iconWrap: {
    width: 96, height: 96, borderRadius: 48,
    backgroundColor: '#FFF8E1', alignItems: 'center', justifyContent: 'center',
    marginBottom: 24,
    shadowColor: GOLD, shadowOffset: { width: 0, height: 4 },
    shadowOpacity: 0.2, shadowRadius: 12, elevation: 4,
  },
  title: { fontSize: 26, fontWeight: '700', color: '#1A1A1A', marginBottom: 12 },
  subtitle: { fontSize: 15, color: '#888', textAlign: 'center', lineHeight: 22, marginBottom: 36 },
  btn: { backgroundColor: GOLD, paddingVertical: 14, paddingHorizontal: 40, borderRadius: 14 },
  btnText: { fontSize: 16, fontWeight: '700', color: '#FFF' },
});
