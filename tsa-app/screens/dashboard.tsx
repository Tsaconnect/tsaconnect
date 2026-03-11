import React, { useState } from 'react';
import {
  View,
  Text,
  ScrollView,
  StyleSheet,
  TouchableOpacity,
} from 'react-native';
import { COLORS, SIZES, FONTS } from '@/constants/theme';
import { Ionicons } from '@expo/vector-icons';
import { SafeAreaView } from "react-native-safe-area-context"
type AssetType = {
  id: string;
  name: string;
  symbol: string;
  quantity: number;
  usdValue: number;
  type: 'mcgp' | 'usdt' | 'usdc';
};

const DebitAccountScreen = () => {
  const [selectedAsset, setSelectedAsset] = useState<string | null>(null);
  const [expandedAsset, setExpandedAsset] = useState<string | null>(null);
  const [isTotalValueHidden, setIsTotalValueHidden] = useState<boolean>(false);

  // Mock data based on the second screenshot
  const assets: AssetType[] = [
    {
      id: '1',
      name: 'MCGP',
      symbol: 'mcgp',
      quantity: 203,
      usdValue: 10,
      type: 'mcgp',
    },
    {
      id: '2',
      name: 'USDC',
      symbol: 'usdc',
      quantity: 203,
      usdValue: 203,
      type: 'usdc',
    },
    {
      id: '3',
      name: 'USDT',
      symbol: 'usdt',
      quantity: 24,
      usdValue: 24,
      type: 'usdt',
    },
  ];

  const handleAssetPress = (assetId: string) => {
    if (expandedAsset === assetId) {
      setExpandedAsset(null);
    } else {
      setExpandedAsset(assetId);
      setSelectedAsset(assetId);
    }
  };

  const toggleTotalValueVisibility = () => {
    setIsTotalValueHidden(!isTotalValueHidden);
  };

  const getTotalValue = () => {
    return assets.reduce((sum, asset) => sum + asset.usdValue, 0);
  };

  const formatCurrency = (amount: number) => {
    return new Intl.NumberFormat('en-US', {
      style: 'currency',
      currency: 'USD',
      minimumFractionDigits: 2,
    }).format(amount);
  };

  const formatNumber = (num: number) => {
    return new Intl.NumberFormat('en-US').format(num);
  };

  const getDisplayTotalValue = () => {
    if (isTotalValueHidden) {
      return '••••••';
    }
    return formatCurrency(getTotalValue());
  };

  return (
    <SafeAreaView style={styles.safeArea}>
      <View style={styles.container}>
        {/* Header */}
        <View style={styles.header}>
          <View style={styles.headerTopRow}>
            <Text style={styles.headerTitle}>Debit Account</Text>
            <TouchableOpacity
              onPress={toggleTotalValueVisibility}
              style={styles.eyeIconButton}
              activeOpacity={0.7}
            >
              <Ionicons
                name={isTotalValueHidden ? 'eye-off-outline' : 'eye-outline'}
                size={24}
                color={COLORS.primary}
              />
            </TouchableOpacity>
          </View>

          <View style={styles.totalValueContainer}>
            <Text style={styles.totalLabel}>Total Value</Text>
            <View style={styles.totalValueRow}>
              <Text style={styles.totalValue}>{getDisplayTotalValue()}</Text>
            </View>
          </View>
        </View>

        {/* Asset List */}
        <ScrollView style={styles.assetList} showsVerticalScrollIndicator={false}>
          <Text style={styles.sectionTitle}>SELECT ASSETS</Text>

          {assets.map((asset) => (
            <TouchableOpacity
              key={asset.id}
              style={[
                styles.assetCard,
                selectedAsset === asset.id && styles.selectedAssetCard,
              ]}
              onPress={() => handleAssetPress(asset.id)}
              activeOpacity={0.7}
            >
              <View style={styles.assetHeader}>
                <View style={styles.assetInfo}>
                  <View style={styles.assetIconContainer}>
                    <Text style={styles.assetIcon}>
                      {asset.symbol.charAt(0).toUpperCase()}
                    </Text>
                  </View>
                  <View>
                    <Text style={styles.assetName}>{asset.name}</Text>
                    <Text style={styles.assetSymbol}>{asset.symbol.toUpperCase()}</Text>
                  </View>
                </View>

                <View style={styles.assetValueContainer}>
                  <Text style={styles.assetValue}>
                    {isTotalValueHidden ? '••••' : formatCurrency(asset.usdValue)}
                  </Text>
                  <Ionicons
                    name={expandedAsset === asset.id ? 'chevron-up' : 'chevron-down'}
                    size={20}
                    color={COLORS.primary}
                    style={styles.chevronIcon}
                  />
                </View>
              </View>

              {/* Expanded Details */}
              {expandedAsset === asset.id && (
                <View style={styles.expandedDetails}>
                  <View style={styles.detailRow}>
                    <Text style={styles.detailLabel}>Asset Quantity:</Text>
                    <Text style={styles.detailValue}>
                      {isTotalValueHidden ? '••••' : `${formatNumber(asset.quantity)} ${asset.symbol.toUpperCase()}`}
                    </Text>
                  </View>
                  <View style={styles.detailRow}>
                    <Text style={styles.detailLabel}>Total Worth:</Text>
                    <Text style={styles.detailValue}>
                      {isTotalValueHidden ? '••••' : formatCurrency(asset.usdValue)}
                    </Text>
                  </View>
                  {!isTotalValueHidden && (
                    <View style={styles.valueBreakdown}>
                      <Text style={styles.breakdownText}>
                        ≈ {formatNumber(asset.quantity)} {asset.symbol.toUpperCase()} ×
                        ${(asset.usdValue / asset.quantity).toFixed(2)} each
                      </Text>
                    </View>
                  )}
                </View>
              )}
            </TouchableOpacity>
          ))}
        </ScrollView>

        {/* Footer Info */}
        <View style={styles.footer}>
          <Text style={styles.footerText}>
            {isTotalValueHidden ? 'Tap the eye icon to show values' : 'Tap any asset to view details'}
          </Text>
        </View>
      </View>
    </SafeAreaView>
  );
};

const styles = StyleSheet.create({
  safeArea: {
    flex: 1,
    backgroundColor: '#fff',
  },
  container: {
    flex: 1,
    backgroundColor: '#fff',
  },
  header: {
    paddingHorizontal: SIZES.padding,
    paddingTop: SIZES.padding,
    paddingBottom: SIZES.padding * 1.5,
    borderBottomWidth: 1,
    borderBottomColor: COLORS.lightGray,
    backgroundColor: '#fff',
  },
  headerTopRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: SIZES.base * 2,
  },
  headerTitle: {
    ...FONTS.h1,
    fontSize: 24,
    fontWeight: 'bold',
    color: COLORS.primary,
  },
  eyeIconButton: {
    padding: SIZES.base * 0.5,
    backgroundColor: COLORS.lightGray + '20',
    borderRadius: SIZES.radius,
    width: 40,
    height: 40,
    justifyContent: 'center',
    alignItems: 'center',
  },
  totalValueContainer: {
    alignItems: 'flex-start',
  },
  totalLabel: {
    ...FONTS.body4,
    color: COLORS.gray,
    marginBottom: SIZES.base * 0.5,
  },
  totalValueRow: {
    flexDirection: 'row',
    alignItems: 'center',
  },
  totalValue: {
    ...FONTS.h2,
    fontSize: 28,
    fontWeight: 'bold',
    color: COLORS.primary,
  },
  sectionTitle: {
    ...FONTS.h4,
    color: COLORS.gray,
    marginBottom: SIZES.padding,
    paddingHorizontal: SIZES.padding,
    textTransform: 'uppercase',
    letterSpacing: 1,
  },
  assetList: {
    flex: 1,
    paddingHorizontal: SIZES.padding,
    paddingTop: SIZES.base,
  },
  assetCard: {
    backgroundColor: '#fff',
    borderRadius: SIZES.radius,
    padding: SIZES.padding,
    marginBottom: SIZES.base * 1.5,
    borderWidth: 1,
    borderColor: COLORS.lightGray,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 1 },
    shadowOpacity: 0.05,
    shadowRadius: 2,
    elevation: 2,
  },
  selectedAssetCard: {
    borderColor: COLORS.primary,
    borderWidth: 1.5,
    backgroundColor: '#f8f9ff',
  },
  assetHeader: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
  },
  assetInfo: {
    flexDirection: 'row',
    alignItems: 'center',
    flex: 1,
  },
  assetIconContainer: {
    width: 40,
    height: 40,
    borderRadius: 20,
    backgroundColor: COLORS.primary + '20',
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: SIZES.base,
  },
  assetIcon: {
    ...FONTS.h3,
    color: COLORS.primary,
    fontWeight: 'bold',
  },
  assetName: {
    ...FONTS.h4,
    fontWeight: '600',
    color: COLORS.dark,
    marginBottom: 2,
  },
  assetSymbol: {
    ...FONTS.body5,
    color: COLORS.gray,
  },
  assetValueContainer: {
    flexDirection: 'row',
    alignItems: 'center',
  },
  assetValue: {
    ...FONTS.h4,
    fontWeight: '600',
    color: COLORS.primary,
    marginRight: SIZES.base,
  },
  chevronIcon: {
    marginLeft: SIZES.base * 0.5,
  },
  expandedDetails: {
    marginTop: SIZES.padding,
    paddingTop: SIZES.padding,
    borderTopWidth: 1,
    borderTopColor: COLORS.lightGray,
  },
  detailRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: SIZES.base,
  },
  detailLabel: {
    ...FONTS.body4,
    color: COLORS.gray,
  },
  detailValue: {
    ...FONTS.body3,
    fontWeight: '500',
    color: COLORS.dark,
  },
  valueBreakdown: {
    backgroundColor: COLORS.lightGray + '30',
    padding: SIZES.base,
    borderRadius: SIZES.radius * 0.5,
    marginTop: SIZES.base,
  },
  breakdownText: {
    ...FONTS.body5,
    color: COLORS.gray,
    fontStyle: 'italic',
    textAlign: 'center',
  },
  footer: {
    padding: SIZES.padding,
    borderTopWidth: 1,
    borderTopColor: COLORS.lightGray,
    alignItems: 'center',
  },
  footerText: {
    ...FONTS.body5,
    color: COLORS.gray,
  },
});

export default DebitAccountScreen;