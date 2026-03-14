// app/screens/TradeAndEarnScreen.tsx
import { router } from 'expo-router';
import React, { useState } from 'react';
import {
  View,
  Text,
  TouchableOpacity,
  ScrollView,
  StyleSheet,
  Platform,
  Share,
  Alert,
  Clipboard,
} from 'react-native';
import Icon from 'react-native-vector-icons/MaterialIcons';
import { SafeAreaView } from "react-native-safe-area-context"
// Gold color palette
const GOLD_COLORS = {
  primary: '#FFD700',
  dark: '#B8860B',
  light: '#FFF8DC',
  muted: '#F5DEB3',
  background: '#FAF9F6',
  error: '#DC3545',
  success: '#28A745',
};

const TradeAndEarnScreen: React.FC = () => {
  const [referralLink, setReferralLink] = useState('https://tsaconnect.com/ref/abc123xyz');
  const [tpBalance, setTpBalance] = useState(100);
  const [showReferralModal, setShowReferralModal] = useState(false);

  const copyReferralLink = () => {
    Clipboard.setString(referralLink);
    Alert.alert('Copied!', 'Referral link copied to clipboard');
  };

  const shareReferralLink = async () => {
    try {
      await Share.share({
        message: `Join TSA Connect and earn with me! Use my referral link: ${referralLink}`,
        title: 'TSA Connect Referral',
      });
    } catch (error) {
      Alert.alert('Error', 'Failed to share referral link');
    }
  };

  const handleStartTrading = () => {
    //@ts-expect-error
    router.push('/marketplace');
  };

  const handleJoinP2P = () => {
    //@ts-expect-error
    router.push('/services');
  };

  return (
    <SafeAreaView style={styles.container}>
      <ScrollView style={styles.scrollContainer}>
        {/* Main Content */}
        <View style={styles.content}>
          {/* Hero Banner */}
          <View style={styles.heroBanner}>
            <Text style={styles.heroTitle}>Earn While You Trade!</Text>
            <Text style={styles.heroSubtitle}>
              Multiple ways to earn rewards from every transaction
            </Text>
          </View>

          {/* Earning Methods */}
          <View style={styles.section}>
            <Text style={styles.sectionTitle}>Earning Methods</Text>

            {/* Method 1: Cashback */}
            <View style={styles.methodCard}>
              <View style={styles.methodHeader}>
                <View style={[styles.methodIcon, { backgroundColor: '#FFF3E0' }]}>
                  <Icon name="payments" size={32} color="#F57C00" />
                </View>
                <View style={styles.methodInfo}>
                  <Text style={styles.methodTitle}>1% Cashback</Text>
                  <Text style={styles.methodRate}>Instant Cashback</Text>
                </View>
              </View>

              <View style={styles.methodDetails}>
                <View style={styles.detailItem}>
                  <Icon name="check-circle" size={16} color={GOLD_COLORS.success} />
                  <Text style={styles.detailText}>
                    Earn 1% cashback on all product/service purchases
                  </Text>
                </View>
                <View style={styles.detailItem}>
                  <Icon name="check-circle" size={16} color={GOLD_COLORS.success} />
                  <Text style={styles.detailText}>
                    Earn 1% OTF when you or downlines swap to MCGP
                  </Text>
                </View>
                <View style={styles.detailItem}>
                  <Icon name="check-circle" size={16} color={GOLD_COLORS.success} />
                  <Text style={styles.detailText}>
                    Payments must be in USDT or USDC
                  </Text>
                </View>
              </View>

              <TouchableOpacity
                style={styles.actionButton}
                onPress={handleStartTrading}
                activeOpacity={0.8}
              >
                <Icon name="shopping-cart" size={20} color="#000000" />
                <Text style={styles.actionButtonText}>Start Trading to Earn Now!</Text>
              </TouchableOpacity>
            </View>

            {/* Method 2: Referral Program */}
            <View style={styles.methodCard}>
              <View style={styles.methodHeader}>
                <View style={[styles.methodIcon, { backgroundColor: '#E8F5E9' }]}>
                  <Icon name="people" size={32} color="#2E7D32" />
                </View>
                <View style={styles.methodInfo}>
                  <Text style={styles.methodTitle}>Referral Program</Text>
                  <Text style={styles.methodRate}>Earn from referrals</Text>
                </View>
              </View>

              <View style={styles.methodDetails}>
                <View style={styles.detailItem}>
                  <Icon name="check-circle" size={16} color={GOLD_COLORS.success} />
                  <Text style={styles.detailText}>
                    Earn from direct referral purchases
                  </Text>
                </View>
                <View style={styles.detailItem}>
                  <Icon name="check-circle" size={16} color={GOLD_COLORS.success} />
                  <Text style={styles.detailText}>
                    Copy and share your referral link
                  </Text>
                </View>
                <View style={styles.detailItem}>
                  <Icon name="check-circle" size={16} color={GOLD_COLORS.success} />
                  <Text style={styles.detailText}>
                    Multiple levels of earning potential
                  </Text>
                </View>
              </View>

              {/* Referral Link Section */}
              <View style={styles.referralSection}>
                <Text style={styles.referralLabel}>Referral Link:</Text>
                <View style={styles.referralLinkContainer}>
                  <Text style={styles.referralLink} numberOfLines={1}>
                    {referralLink}
                  </Text>
                  <TouchableOpacity
                    style={styles.copyButton}
                    onPress={copyReferralLink}
                  >
                    <Icon name="content-copy" size={20} color={GOLD_COLORS.dark} />
                  </TouchableOpacity>
                  <TouchableOpacity
                    style={styles.shareButton}
                    onPress={shareReferralLink}
                  >
                    <Icon name="share" size={20} color={GOLD_COLORS.dark} />
                  </TouchableOpacity>
                </View>
                <Text style={styles.referralNote}>
                  Copy referral link and onboard others to earn too
                </Text>
              </View>
            </View>

            {/* Method 3: P2P Trading Commission */}
            <View style={styles.methodCard}>
              <View style={styles.methodHeader}>
                <View style={[styles.methodIcon, { backgroundColor: '#E3F2FD' }]}>
                  <Icon name="swap-horiz" size={32} color="#1976D2" />
                </View>
                <View style={styles.methodInfo}>
                  <Text style={styles.methodTitle}>P2P Commission</Text>
                  <Text style={styles.methodRate}>0.5% Commission</Text>
                </View>
              </View>

              <View style={styles.methodDetails}>
                <View style={styles.detailItem}>
                  <Icon name="check-circle" size={16} color={GOLD_COLORS.success} />
                  <Text style={styles.detailText}>
                    Earn 0.5% from P2P fiat deposit/withdrawal trades
                  </Text>
                </View>
                <View style={styles.detailItem}>
                  <Icon name="check-circle" size={16} color={GOLD_COLORS.success} />
                  <Text style={styles.detailText}>
                    Available for internal sellers & external buyers
                  </Text>
                </View>
                <View style={styles.detailItem}>
                  <Icon name="check-circle" size={16} color={GOLD_COLORS.success} />
                  <Text style={styles.detailText}>
                    Commission from every successful USDT trade
                  </Text>
                </View>
              </View>

              <TouchableOpacity
                style={styles.actionButton}
                onPress={handleJoinP2P}
                activeOpacity={0.8}
              >
                <Icon name="store" size={20} color="#000000" />
                <Text style={styles.actionButtonText}>
                  Join TSA Connect P2P Trading Merchant Service
                </Text>
              </TouchableOpacity>
            </View>
          </View>

          {/* Trade Points Section */}
          <View style={styles.section}>
            <Text style={styles.sectionTitle}>Trade Points (TP)</Text>

            <View style={styles.tpCard}>
              <View style={styles.tpHeader}>
                <View style={styles.tpIconContainer}>
                  <Icon name="stars" size={32} color={GOLD_COLORS.primary} />
                </View>
                <View style={styles.tpBalanceContainer}>
                  <Text style={styles.tpBalanceLabel}>TP Balance</Text>
                  <Text style={styles.tpBalanceValue}>{tpBalance} TPs</Text>
                </View>
              </View>

              <View style={styles.tpConversion}>
                <Text style={styles.tpConversionTitle}>TP Conversion Rates:</Text>
                <View style={styles.conversionRow}>
                  <Icon name="currency-exchange" size={16} color={GOLD_COLORS.dark} />
                  <Text style={styles.conversionText}>0.01 TP = $0.1 trades</Text>
                </View>
                <View style={styles.conversionRow}>
                  <Icon name="currency-exchange" size={16} color={GOLD_COLORS.dark} />
                  <Text style={styles.conversionText}>0.1 TP = $1 trades</Text>
                </View>
                <View style={styles.conversionRow}>
                  <Icon name="currency-exchange" size={16} color={GOLD_COLORS.dark} />
                  <Text style={styles.conversionText}>1 TP = $10 trades</Text>
                </View>
              </View>

              <View style={styles.tpNote}>
                <Icon name="info" size={16} color={GOLD_COLORS.dark} />
                <Text style={styles.tpNoteText}>
                  Minimum purchase to gain TP is $0.1 buy/sell activities.
                  TPs will be converted to cash in future. Accumulate as much as possible through active trading.
                </Text>
              </View>
            </View>
          </View>

          {/* Statistics Dashboard */}
          <View style={styles.section}>
            <Text style={styles.sectionTitle}>Your Earnings Dashboard</Text>

            <View style={styles.statsGrid}>
              <View style={styles.statCard}>
                <Text style={styles.statValue}>$0.00</Text>
                <Text style={styles.statLabel}>Total Cashback</Text>
                <View style={[styles.statIcon, { backgroundColor: '#FFF3E0' }]}>
                  <Icon name="payments" size={20} color="#F57C00" />
                </View>
              </View>

              <View style={styles.statCard}>
                <Text style={styles.statValue}>0</Text>
                <Text style={styles.statLabel}>Total Referrals</Text>
                <View style={[styles.statIcon, { backgroundColor: '#E8F5E9' }]}>
                  <Icon name="people" size={20} color="#2E7D32" />
                </View>
              </View>

              <View style={styles.statCard}>
                <Text style={styles.statValue}>$0.00</Text>
                <Text style={styles.statLabel}>P2P Commission</Text>
                <View style={[styles.statIcon, { backgroundColor: '#E3F2FD' }]}>
                  <Icon name="swap-horiz" size={20} color="#1976D2" />
                </View>
              </View>

              <View style={styles.statCard}>
                <Text style={styles.statValue}>{tpBalance}</Text>
                <Text style={styles.statLabel}>Trade Points</Text>
                <View style={[styles.statIcon, { backgroundColor: '#F3E5F5' }]}>
                  <Icon name="stars" size={20} color="#7B1FA2" />
                </View>
              </View>
            </View>
          </View>

          {/* How It Works */}
          <View style={styles.section}>
            <Text style={styles.sectionTitle}>How It Works</Text>

            <View style={styles.stepsContainer}>
              <View style={styles.step}>
                <View style={styles.stepNumber}>
                  <Text style={styles.stepNumberText}>1</Text>
                </View>
                <View style={styles.stepContent}>
                  <Text style={styles.stepTitle}>Start Trading</Text>
                  <Text style={styles.stepDescription}>
                    Buy or sell products/services in the marketplace
                  </Text>
                </View>
              </View>

              <View style={styles.stepDivider}>
                <Icon name="arrow-downward" size={20} color={GOLD_COLORS.dark} />
              </View>

              <View style={styles.step}>
                <View style={styles.stepNumber}>
                  <Text style={styles.stepNumberText}>2</Text>
                </View>
                <View style={styles.stepContent}>
                  <Text style={styles.stepTitle}>Earn Rewards</Text>
                  <Text style={styles.stepDescription}>
                    Automatically earn cashback, TPs, and commissions
                  </Text>
                </View>
              </View>

              <View style={styles.stepDivider}>
                <Icon name="arrow-downward" size={20} color={GOLD_COLORS.dark} />
              </View>

              <View style={styles.step}>
                <View style={styles.stepNumber}>
                  <Text style={styles.stepNumberText}>3</Text>
                </View>
                <View style={styles.stepContent}>
                  <Text style={styles.stepTitle}>Invite & Earn More</Text>
                  <Text style={styles.stepDescription}>
                    Share referral link to earn from your network
                  </Text>
                </View>
              </View>

              <View style={styles.stepDivider}>
                <Icon name="arrow-downward" size={20} color={GOLD_COLORS.dark} />
              </View>

              <View style={styles.step}>
                <View style={styles.stepNumber}>
                  <Text style={styles.stepNumberText}>4</Text>
                </View>
                <View style={styles.stepContent}>
                  <Text style={styles.stepTitle}>Join P2P</Text>
                  <Text style={styles.stepDescription}>
                    Become a merchant for additional commission
                  </Text>
                </View>
              </View>
            </View>
          </View>

          {/* Quick Actions */}
          <View style={styles.quickActions}>
            <TouchableOpacity
              style={styles.primaryAction}
              onPress={handleStartTrading}
              activeOpacity={0.8}
            >
              <Icon name="shopping-cart" size={24} color="#000000" />
              <Text style={styles.primaryActionText}>Start Trading Now</Text>
            </TouchableOpacity>

            <TouchableOpacity
              style={styles.secondaryAction}
              onPress={handleJoinP2P}
              activeOpacity={0.8}
            >
              <Icon name="store" size={24} color="#000000" />
              <Text style={styles.secondaryActionText}>Join P2P Merchant</Text>
            </TouchableOpacity>
          </View>
        </View>
      </ScrollView>
    </SafeAreaView>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: GOLD_COLORS.background,
  },
  scrollContainer: {
    flex: 1,
  },
  header: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingHorizontal: 20,
    paddingTop: Platform.OS === 'ios' ? 10 : 20,
    paddingBottom: 20,
    backgroundColor: '#FFFFFF',
    borderBottomWidth: 1,
    borderBottomColor: '#F0F0F0',
  },
  backButton: {
    padding: 8,
    marginRight: 12,
  },
  headerTitle: {
    fontSize: 24,
    fontWeight: '800',
    color: '#000000',
    flex: 1,
  },
  helpButton: {
    padding: 8,
  },
  content: {
    padding: 20,
  },
  heroBanner: {
    backgroundColor: GOLD_COLORS.primary,
    borderRadius: 20,
    padding: 24,
    marginBottom: 24,
    alignItems: 'center',
  },
  heroTitle: {
    fontSize: 28,
    fontWeight: '900',
    color: '#000000',
    marginBottom: 8,
    textAlign: 'center',
  },
  heroSubtitle: {
    fontSize: 16,
    color: '#000000',
    opacity: 0.8,
    textAlign: 'center',
  },
  section: {
    marginBottom: 32,
  },
  sectionTitle: {
    fontSize: 22,
    fontWeight: '800',
    color: '#000000',
    marginBottom: 16,
  },
  methodCard: {
    backgroundColor: '#FFFFFF',
    borderRadius: 16,
    padding: 20,
    marginBottom: 20,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.05,
    shadowRadius: 4,
    elevation: 2,
  },
  methodHeader: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 16,
  },
  methodIcon: {
    width: 56,
    height: 56,
    borderRadius: 28,
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: 16,
  },
  methodInfo: {
    flex: 1,
  },
  methodTitle: {
    fontSize: 20,
    fontWeight: '800',
    color: '#000000',
    marginBottom: 4,
  },
  methodRate: {
    fontSize: 16,
    color: GOLD_COLORS.dark,
    fontWeight: '600',
  },
  methodDetails: {
    marginBottom: 20,
  },
  detailItem: {
    flexDirection: 'row',
    alignItems: 'flex-start',
    marginBottom: 8,
  },
  detailText: {
    fontSize: 14,
    color: '#666666',
    marginLeft: 8,
    flex: 1,
    lineHeight: 20,
  },
  actionButton: {
    backgroundColor: GOLD_COLORS.primary,
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    borderRadius: 12,
    paddingVertical: 16,
    shadowColor: GOLD_COLORS.dark,
    shadowOffset: { width: 0, height: 4 },
    shadowOpacity: 0.3,
    shadowRadius: 8,
    elevation: 6,
  },
  actionButtonText: {
    fontSize: 16,
    fontWeight: '800',
    color: '#000000',
    marginLeft: 8,
    textAlign: 'center',
    flex: 1,
  },
  referralSection: {
    marginTop: 16,
  },
  referralLabel: {
    fontSize: 16,
    fontWeight: '600',
    color: '#000000',
    marginBottom: 8,
  },
  referralLinkContainer: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 12,
    padding: 12,
    marginBottom: 8,
  },
  referralLink: {
    flex: 1,
    fontSize: 14,
    color: '#666666',
    fontFamily: Platform.OS === 'ios' ? 'Menlo' : 'monospace',
  },
  copyButton: {
    padding: 8,
    marginLeft: 8,
  },
  shareButton: {
    padding: 8,
    marginLeft: 4,
  },
  referralNote: {
    fontSize: 12,
    color: GOLD_COLORS.dark,
    fontStyle: 'italic',
  },
  tpCard: {
    backgroundColor: '#FFFFFF',
    borderRadius: 16,
    padding: 20,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.05,
    shadowRadius: 4,
    elevation: 2,
  },
  tpHeader: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 20,
  },
  tpIconContainer: {
    width: 64,
    height: 64,
    borderRadius: 32,
    backgroundColor: GOLD_COLORS.light,
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: 16,
  },
  tpBalanceContainer: {
    flex: 1,
  },
  tpBalanceLabel: {
    fontSize: 14,
    color: '#666666',
    marginBottom: 4,
  },
  tpBalanceValue: {
    fontSize: 32,
    fontWeight: '900',
    color: GOLD_COLORS.primary,
  },
  tpConversion: {
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 12,
    padding: 16,
    marginBottom: 16,
  },
  tpConversionTitle: {
    fontSize: 16,
    fontWeight: '700',
    color: '#000000',
    marginBottom: 12,
  },
  conversionRow: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 8,
  },
  conversionText: {
    fontSize: 14,
    color: '#666666',
    marginLeft: 8,
  },
  tpNote: {
    flexDirection: 'row',
    alignItems: 'flex-start',
    backgroundColor: '#FFF8E1',
    padding: 16,
    borderRadius: 12,
  },
  tpNoteText: {
    fontSize: 14,
    color: GOLD_COLORS.dark,
    marginLeft: 8,
    flex: 1,
    lineHeight: 20,
  },
  statsGrid: {
    flexDirection: 'row',
    flexWrap: 'wrap',
    gap: 12,
  },
  statCard: {
    width: '48%',
    backgroundColor: '#FFFFFF',
    borderRadius: 16,
    padding: 20,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.05,
    shadowRadius: 4,
    elevation: 2,
  },
  statValue: {
    fontSize: 24,
    fontWeight: '900',
    color: '#000000',
    marginBottom: 4,
  },
  statLabel: {
    fontSize: 14,
    color: '#666666',
    marginBottom: 12,
  },
  statIcon: {
    width: 40,
    height: 40,
    borderRadius: 20,
    justifyContent: 'center',
    alignItems: 'center',
    position: 'absolute',
    top: 16,
    right: 16,
  },
  stepsContainer: {
    backgroundColor: '#FFFFFF',
    borderRadius: 16,
    padding: 20,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.05,
    shadowRadius: 4,
    elevation: 2,
  },
  step: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 8,
  },
  stepNumber: {
    width: 40,
    height: 40,
    borderRadius: 20,
    backgroundColor: GOLD_COLORS.primary,
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: 16,
  },
  stepNumberText: {
    fontSize: 18,
    fontWeight: '900',
    color: '#000000',
  },
  stepContent: {
    flex: 1,
  },
  stepTitle: {
    fontSize: 16,
    fontWeight: '700',
    color: '#000000',
    marginBottom: 4,
  },
  stepDescription: {
    fontSize: 14,
    color: '#666666',
  },
  stepDivider: {
    alignItems: 'center',
    marginVertical: 8,
  },
  quickActions: {
    flexDirection: 'row',
    gap: 12,
    marginTop: 24,
  },
  primaryAction: {
    flex: 1,
    backgroundColor: GOLD_COLORS.primary,
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    borderRadius: 16,
    paddingVertical: 18,
    shadowColor: GOLD_COLORS.dark,
    shadowOffset: { width: 0, height: 4 },
    shadowOpacity: 0.3,
    shadowRadius: 8,
    elevation: 6,
  },
  primaryActionText: {
    fontSize: 18,
    fontWeight: '800',
    color: '#000000',
    marginLeft: 8,
  },
  secondaryAction: {
    flex: 1,
    backgroundColor: '#FFFFFF',
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    borderRadius: 16,
    paddingVertical: 18,
    borderWidth: 2,
    borderColor: GOLD_COLORS.primary,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.05,
    shadowRadius: 4,
    elevation: 2,
  },
  secondaryActionText: {
    fontSize: 16,
    fontWeight: '800',
    color: '#000000',
    marginLeft: 8,
  },
});

export default TradeAndEarnScreen;