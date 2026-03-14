import React, { useState, useEffect } from 'react';
import {
  View,
  Text,
  TouchableOpacity,
  StyleSheet,
  Share,
  Alert,
} from 'react-native';
import { router } from 'expo-router';
import AsyncStorage from '@react-native-async-storage/async-storage';
import * as Clipboard from 'expo-clipboard';
import QRCode from 'react-native-qrcode-svg';
import { COLORS, SIZES, FONTS, SHADOWS } from '../../constants';

const ReceiveToken = () => {
  const [walletAddress, setWalletAddress] = useState('');
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    const loadAddress = async () => {
      const address = await AsyncStorage.getItem('walletAddress');
      if (address) setWalletAddress(address);
    };
    loadAddress();
  }, []);

  const handleCopy = async () => {
    if (!walletAddress) return;
    await Clipboard.setStringAsync(walletAddress);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  const handleShare = async () => {
    if (!walletAddress) return;
    try {
      await Share.share({
        message: `My TSA Connect wallet address: ${walletAddress}`,
      });
    } catch (err) {
      console.error('Share error:', err);
    }
  };

  return (
    <View style={styles.container}>
      <View style={styles.header}>
        <Text style={styles.subtitle}>
          Share your address or QR code to receive tokens on the Sonic network.
        </Text>
      </View>

      <View style={styles.qrContainer}>
        {walletAddress ? (
          <View style={styles.qrWrapper}>
            <QRCode
              value={walletAddress}
              size={200}
              color={COLORS.dark}
              backgroundColor={COLORS.white}
            />
          </View>
        ) : (
          <View style={[styles.qrWrapper, { justifyContent: 'center', alignItems: 'center' }]}>
            <Text style={styles.placeholderText}>No wallet address</Text>
          </View>
        )}
      </View>

      <View style={styles.addressContainer}>
        <Text style={styles.addressLabel}>Your Wallet Address</Text>
        <Text style={styles.addressText} selectable numberOfLines={2}>
          {walletAddress || 'Not available'}
        </Text>
      </View>

      <View style={styles.buttonRow}>
        <TouchableOpacity style={styles.copyButton} onPress={handleCopy}>
          <Text style={styles.copyButtonText}>
            {copied ? 'Copied!' : 'Copy Address'}
          </Text>
        </TouchableOpacity>
        <TouchableOpacity style={styles.shareButton} onPress={handleShare}>
          <Text style={styles.shareButtonText}>Share</Text>
        </TouchableOpacity>
      </View>

      <View style={styles.infoBox}>
        <Text style={styles.infoText}>
          Only send tokens on the Sonic network to this address. Sending tokens from other
          networks may result in permanent loss.
        </Text>
      </View>
    </View>
  );
};

export default ReceiveToken;

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: COLORS.white,
    padding: SIZES.padding3,
  },
  header: {
    marginBottom: 32,
  },
  subtitle: {
    ...FONTS.body3,
    color: COLORS.gray,
    lineHeight: 22,
  },
  qrContainer: {
    alignItems: 'center',
    marginBottom: 32,
  },
  qrWrapper: {
    padding: 24,
    backgroundColor: COLORS.white,
    borderRadius: 16,
    borderWidth: 1,
    borderColor: COLORS.lightGray,
    ...SHADOWS.medium,
  },
  placeholderText: {
    ...FONTS.body3,
    color: COLORS.gray,
    width: 200,
    height: 200,
    textAlign: 'center',
    textAlignVertical: 'center',
    lineHeight: 200,
  },
  addressContainer: {
    backgroundColor: COLORS.lightGray,
    borderRadius: 12,
    padding: 16,
    marginBottom: 24,
    alignItems: 'center',
  },
  addressLabel: {
    ...FONTS.body5,
    color: COLORS.gray,
    marginBottom: 8,
  },
  addressText: {
    ...FONTS.body4,
    color: COLORS.dark,
    fontWeight: '500',
    textAlign: 'center',
  },
  buttonRow: {
    flexDirection: 'row',
    gap: 12,
    marginBottom: 24,
  },
  copyButton: {
    flex: 1,
    backgroundColor: COLORS.primary,
    paddingVertical: 16,
    borderRadius: 12,
    alignItems: 'center',
    ...SHADOWS.medium,
  },
  copyButtonText: {
    ...FONTS.h4,
    color: COLORS.white,
    fontWeight: '600',
  },
  shareButton: {
    flex: 1,
    backgroundColor: COLORS.white,
    paddingVertical: 16,
    borderRadius: 12,
    alignItems: 'center',
    borderWidth: 2,
    borderColor: COLORS.primary,
  },
  shareButtonText: {
    ...FONTS.h4,
    color: COLORS.primary,
    fontWeight: '600',
  },
  infoBox: {
    backgroundColor: '#FEF3C7',
    borderRadius: 12,
    padding: 16,
  },
  infoText: {
    ...FONTS.body4,
    color: '#92400E',
    lineHeight: 20,
  },
});
