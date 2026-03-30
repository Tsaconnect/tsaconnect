// app/screens/TradeAndEarnScreen.tsx
import { router } from 'expo-router';
import React, { useEffect, useState } from 'react';
import {
  View,
  Text,
  TouchableOpacity,
  ScrollView,
  StyleSheet,
  Share,
  Alert,
  RefreshControl,
} from 'react-native';
import Icon from 'react-native-vector-icons/MaterialIcons';
import { SafeAreaView } from 'react-native-safe-area-context';
import * as Clipboard from 'expo-clipboard';
import api from '../components/services/api';

const COLORS = {
  primary: '#FFD700',
  dark: '#B8860B',
  light: '#FFF8DC',
  background: '#F5F5F5',
  white: '#FFFFFF',
  text: '#1A1A1A',
  textSecondary: '#666666',
  textMuted: '#999999',
  success: '#28A745',
  border: '#F0F0F0',
};

const TradeAndEarnScreen: React.FC = () => {
  const [referralCode, setReferralCode] = useState('');
  const [tpBalance, setTpBalance] = useState(0);
  const [totalReferrals, setTotalReferrals] = useState(0);
  const [refreshing, setRefreshing] = useState(false);

  const fetchData = async () => {
    try {
      const [profileRes, tpRes, referralsRes] = await Promise.all([
        api.getProfile(),
        api.getTPBalance(),
        api.getReferrals(),
      ]);

      if (profileRes.success && profileRes.data) {
        setReferralCode(profileRes.data.referralCode || profileRes.data.username || '');
      }
      if (tpRes.success && tpRes.data) {
        setTpBalance(tpRes.data.tpBalance || 0);
      }
      if (referralsRes.success && referralsRes.data) {
        setTotalReferrals(referralsRes.data.totalReferrals || 0);
      }
    } catch (error) {
      console.error('Error fetching trade & earn data:', error);
    } finally {
      setRefreshing(false);
    }
  };

  useEffect(() => {
    fetchData();
  }, []);

  const onRefresh = () => {
    setRefreshing(true);
    fetchData();
  };

  const copyReferralCode = () => {
    if (!referralCode) {
      Alert.alert('Error', 'No referral code available');
      return;
    }
    Clipboard.setStringAsync(referralCode);
    Alert.alert('Copied!', 'Referral code copied to clipboard');
  };

  const shareReferralCode = async () => {
    if (!referralCode) return;
    try {
      await Share.share({
        message: `Join TSA Connect using my referral code: ${referralCode}`,
        title: 'TSA Connect Referral',
      });
    } catch (error) {
      Alert.alert('Error', 'Failed to share referral code');
    }
  };

  return (
    <SafeAreaView style={styles.container}>
      <ScrollView
        showsVerticalScrollIndicator={false}
        refreshControl={
          <RefreshControl refreshing={refreshing} onRefresh={onRefresh} />
        }
      >
        <View style={styles.content}>
          {/* Hero Card — TP Balance + Referral Code */}
          <View style={styles.heroCard}>
            <View style={styles.heroTop}>
              <View>
                <Text style={styles.heroLabel}>Trade Points</Text>
                <Text style={styles.heroBalance}>{tpBalance.toFixed(2)}</Text>
                <Text style={styles.heroUnit}>TP</Text>
              </View>
              <View style={styles.heroIconWrap}>
                <Icon name="stars" size={36} color={COLORS.dark} />
              </View>
            </View>

            <View style={styles.heroDivider} />

            <View style={styles.referralRow}>
              <View style={styles.referralInfo}>
                <Text style={styles.referralLabel}>Your Referral Code</Text>
                <Text style={styles.referralCode}>{referralCode || 'N/A'}</Text>
              </View>
              <View style={styles.referralActions}>
                <TouchableOpacity style={styles.referralBtn} onPress={copyReferralCode}>
                  <Icon name="content-copy" size={18} color={COLORS.dark} />
                </TouchableOpacity>
                <TouchableOpacity style={styles.referralBtn} onPress={shareReferralCode}>
                  <Icon name="share" size={18} color={COLORS.dark} />
                </TouchableOpacity>
              </View>
            </View>
          </View>

          {/* Stats Row */}
          <View style={styles.statsRow}>
            <TouchableOpacity
              style={styles.statItem}
              onPress={() => router.push('/marketplace')}
            >
              <View style={[styles.statDot, { backgroundColor: '#FFF3E0' }]}>
                <Icon name="payments" size={16} color="#F57C00" />
              </View>
              <Text style={styles.statValue}>$0.00</Text>
              <Text style={styles.statLabel}>Cashback</Text>
            </TouchableOpacity>

            <TouchableOpacity
              style={styles.statItem}
              onPress={() => router.push('/referrals')}
            >
              <View style={[styles.statDot, { backgroundColor: '#E8F5E9' }]}>
                <Icon name="people" size={16} color="#2E7D32" />
              </View>
              <Text style={styles.statValue}>{totalReferrals}</Text>
              <Text style={styles.statLabel}>Referrals</Text>
            </TouchableOpacity>

            <View style={styles.statItem}>
              <View style={[styles.statDot, { backgroundColor: '#E3F2FD' }]}>
                <Icon name="swap-horiz" size={16} color="#1976D2" />
              </View>
              <Text style={styles.statValue}>$0.00</Text>
              <Text style={styles.statLabel}>P2P</Text>
            </View>

            <View style={styles.statItem}>
              <View style={[styles.statDot, { backgroundColor: '#F3E5F5' }]}>
                <Icon name="stars" size={16} color="#7B1FA2" />
              </View>
              <Text style={styles.statValue}>{tpBalance.toFixed(2)}</Text>
              <Text style={styles.statLabel}>TP</Text>
            </View>
          </View>

          {/* Earning Methods */}
          <Text style={styles.sectionTitle}>Ways to Earn</Text>

          <View style={styles.methodsCard}>
            <TouchableOpacity
              style={styles.methodRow}
              activeOpacity={0.7}
              onPress={() => router.push('/marketplace')}
            >
              <View style={[styles.methodIcon, { backgroundColor: '#FFF3E0' }]}>
                <Icon name="payments" size={22} color="#F57C00" />
              </View>
              <View style={styles.methodInfo}>
                <Text style={styles.methodTitle}>Cashback</Text>
                <Text style={styles.methodDesc}>
                  Earn on every product & service purchase
                </Text>
              </View>
              <Icon name="chevron-right" size={22} color={COLORS.textMuted} />
            </TouchableOpacity>

            <View style={styles.methodDivider} />

            <TouchableOpacity
              style={styles.methodRow}
              activeOpacity={0.7}
              onPress={() => router.push('/referrals')}
            >
              <View style={[styles.methodIcon, { backgroundColor: '#E8F5E9' }]}>
                <Icon name="people" size={22} color="#2E7D32" />
              </View>
              <View style={styles.methodInfo}>
                <Text style={styles.methodTitle}>Referral Program</Text>
                <Text style={styles.methodDesc}>
                  Earn TP when your referrals trade
                </Text>
              </View>
              <Icon name="chevron-right" size={22} color={COLORS.textMuted} />
            </TouchableOpacity>

            <View style={styles.methodDivider} />

            <TouchableOpacity
              style={styles.methodRow}
              activeOpacity={0.7}
              onPress={() => router.push('/services')}
            >
              <View style={[styles.methodIcon, { backgroundColor: '#E3F2FD' }]}>
                <Icon name="swap-horiz" size={22} color="#1976D2" />
              </View>
              <View style={styles.methodInfo}>
                <Text style={styles.methodTitle}>P2P Commission</Text>
                <Text style={styles.methodDesc}>
                  0.5% from P2P fiat trades as a merchant
                </Text>
              </View>
              <Icon name="chevron-right" size={22} color={COLORS.textMuted} />
            </TouchableOpacity>
          </View>

          {/* Earning Details */}
          <Text style={styles.sectionTitle}>Earning Details</Text>

          <View style={styles.detailsCard}>
            <View style={styles.detailSection}>
              <View style={styles.detailHeader}>
                <Icon name="payments" size={18} color="#F57C00" />
                <Text style={styles.detailTitle}>Cashback</Text>
              </View>
              <Text style={styles.detailText}>
                Earn 1% cashback on all product/service purchases. Payments must be in USDT or USDC.
              </Text>
            </View>

            <View style={styles.detailDivider} />

            <View style={styles.detailSection}>
              <View style={styles.detailHeader}>
                <Icon name="people" size={18} color="#2E7D32" />
                <Text style={styles.detailTitle}>Referral Earnings</Text>
              </View>
              <Text style={styles.detailText}>
                Earn TP from up to 10 generations of your referral network. Every time someone in your network trades, you earn automatically.
              </Text>
            </View>

            <View style={styles.detailDivider} />

            <View style={styles.detailSection}>
              <View style={styles.detailHeader}>
                <Icon name="swap-horiz" size={18} color="#1976D2" />
                <Text style={styles.detailTitle}>P2P Commission</Text>
              </View>
              <Text style={styles.detailText}>
                Earn 0.5% commission from P2P fiat deposit/withdrawal trades. Available for internal sellers and external buyers.
              </Text>
            </View>
          </View>

          {/* TP Conversion Rates */}
          <Text style={styles.sectionTitle}>TP Conversion Rates</Text>

          <View style={styles.conversionCard}>
            <View style={styles.conversionRow}>
              <Icon name="currency-exchange" size={16} color={COLORS.dark} />
              <Text style={styles.conversionText}>0.01 TP = $0.1 trades</Text>
            </View>
            <View style={styles.conversionRow}>
              <Icon name="currency-exchange" size={16} color={COLORS.dark} />
              <Text style={styles.conversionText}>0.1 TP = $1 trades</Text>
            </View>
            <View style={styles.conversionRow}>
              <Icon name="currency-exchange" size={16} color={COLORS.dark} />
              <Text style={styles.conversionText}>1 TP = $10 trades</Text>
            </View>
            <View style={styles.conversionNote}>
              <Icon name="info-outline" size={14} color={COLORS.dark} />
              <Text style={styles.conversionNoteText}>
                Minimum purchase to gain TP is $0.1. TPs will be converted to cash in future.
              </Text>
            </View>
          </View>

          {/* How It Works */}
          <Text style={styles.sectionTitle}>How It Works</Text>

          <View style={styles.stepsCard}>
            <View style={styles.stepRow}>
              <View style={styles.stepNum}><Text style={styles.stepNumText}>1</Text></View>
              <View style={styles.stepInfo}>
                <Text style={styles.stepTitle}>Start Trading</Text>
                <Text style={styles.stepDesc}>Buy or sell products/services in the marketplace</Text>
              </View>
            </View>
            <View style={styles.stepRow}>
              <View style={styles.stepNum}><Text style={styles.stepNumText}>2</Text></View>
              <View style={styles.stepInfo}>
                <Text style={styles.stepTitle}>Earn Rewards</Text>
                <Text style={styles.stepDesc}>Automatically earn cashback, TPs, and commissions</Text>
              </View>
            </View>
            <View style={styles.stepRow}>
              <View style={styles.stepNum}><Text style={styles.stepNumText}>3</Text></View>
              <View style={styles.stepInfo}>
                <Text style={styles.stepTitle}>Invite & Earn More</Text>
                <Text style={styles.stepDesc}>Share your referral code to earn from your network</Text>
              </View>
            </View>
            <View style={styles.stepRow}>
              <View style={styles.stepNum}><Text style={styles.stepNumText}>4</Text></View>
              <View style={styles.stepInfo}>
                <Text style={styles.stepTitle}>Join P2P</Text>
                <Text style={styles.stepDesc}>Become a merchant for additional commission</Text>
              </View>
            </View>
          </View>

          {/* CTA */}
          <TouchableOpacity
            style={styles.ctaButton}
            onPress={() => router.push('/marketplace')}
            activeOpacity={0.8}
          >
            <Icon name="shopping-cart" size={22} color="#000" />
            <Text style={styles.ctaText}>Start Trading Now</Text>
          </TouchableOpacity>

          <TouchableOpacity
            style={styles.ctaSecondary}
            onPress={() => router.push('/services')}
            activeOpacity={0.8}
          >
            <Icon name="store" size={22} color={COLORS.dark} />
            <Text style={styles.ctaSecondaryText}>Become a P2P Merchant</Text>
          </TouchableOpacity>
        </View>
      </ScrollView>
    </SafeAreaView>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: COLORS.background,
  },
  content: {
    padding: 16,
  },

  // Hero Card
  heroCard: {
    backgroundColor: COLORS.primary,
    borderRadius: 20,
    padding: 24,
    marginBottom: 16,
  },
  heroTop: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'flex-start',
  },
  heroLabel: {
    fontSize: 14,
    color: COLORS.text,
    opacity: 0.7,
    marginBottom: 4,
  },
  heroBalance: {
    fontSize: 40,
    fontWeight: '900',
    color: COLORS.text,
    lineHeight: 44,
  },
  heroUnit: {
    fontSize: 16,
    fontWeight: '700',
    color: COLORS.dark,
    marginTop: 2,
  },
  heroIconWrap: {
    width: 56,
    height: 56,
    borderRadius: 28,
    backgroundColor: 'rgba(255,255,255,0.4)',
    justifyContent: 'center',
    alignItems: 'center',
  },
  heroDivider: {
    height: 1,
    backgroundColor: 'rgba(0,0,0,0.1)',
    marginVertical: 16,
  },
  referralRow: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
  },
  referralInfo: {
    flex: 1,
  },
  referralLabel: {
    fontSize: 12,
    color: COLORS.text,
    opacity: 0.6,
    marginBottom: 2,
  },
  referralCode: {
    fontSize: 18,
    fontWeight: '800',
    color: COLORS.text,
  },
  referralActions: {
    flexDirection: 'row',
    gap: 8,
  },
  referralBtn: {
    width: 40,
    height: 40,
    borderRadius: 12,
    backgroundColor: 'rgba(255,255,255,0.5)',
    justifyContent: 'center',
    alignItems: 'center',
  },

  // Stats Row
  statsRow: {
    flexDirection: 'row',
    backgroundColor: COLORS.white,
    borderRadius: 16,
    padding: 16,
    marginBottom: 24,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 1 },
    shadowOpacity: 0.04,
    shadowRadius: 3,
    elevation: 1,
  },
  statItem: {
    flex: 1,
    alignItems: 'center',
  },
  statDot: {
    width: 32,
    height: 32,
    borderRadius: 16,
    justifyContent: 'center',
    alignItems: 'center',
    marginBottom: 8,
  },
  statValue: {
    fontSize: 16,
    fontWeight: '800',
    color: COLORS.text,
    marginBottom: 2,
  },
  statLabel: {
    fontSize: 11,
    color: COLORS.textMuted,
  },

  // Section
  sectionTitle: {
    fontSize: 18,
    fontWeight: '800',
    color: COLORS.text,
    marginBottom: 12,
  },

  // Methods
  methodsCard: {
    backgroundColor: COLORS.white,
    borderRadius: 16,
    overflow: 'hidden',
    marginBottom: 16,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 1 },
    shadowOpacity: 0.04,
    shadowRadius: 3,
    elevation: 1,
  },
  methodRow: {
    flexDirection: 'row',
    alignItems: 'center',
    padding: 16,
  },
  methodIcon: {
    width: 44,
    height: 44,
    borderRadius: 12,
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: 14,
  },
  methodInfo: {
    flex: 1,
  },
  methodTitle: {
    fontSize: 15,
    fontWeight: '700',
    color: COLORS.text,
    marginBottom: 2,
  },
  methodDesc: {
    fontSize: 13,
    color: COLORS.textSecondary,
  },
  methodDivider: {
    height: 1,
    backgroundColor: COLORS.border,
    marginLeft: 74,
  },

  // Earning Details
  detailsCard: {
    backgroundColor: COLORS.white,
    borderRadius: 16,
    padding: 20,
    marginBottom: 24,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 1 },
    shadowOpacity: 0.04,
    shadowRadius: 3,
    elevation: 1,
  },
  detailSection: {
    paddingVertical: 4,
  },
  detailHeader: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 8,
    marginBottom: 6,
  },
  detailTitle: {
    fontSize: 15,
    fontWeight: '700',
    color: COLORS.text,
  },
  detailText: {
    fontSize: 13,
    color: COLORS.textSecondary,
    lineHeight: 19,
  },
  detailDivider: {
    height: 1,
    backgroundColor: COLORS.border,
    marginVertical: 14,
  },

  // Conversion Rates
  conversionCard: {
    backgroundColor: COLORS.white,
    borderRadius: 16,
    padding: 20,
    marginBottom: 24,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 1 },
    shadowOpacity: 0.04,
    shadowRadius: 3,
    elevation: 1,
  },
  conversionRow: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 10,
    marginBottom: 10,
  },
  conversionText: {
    fontSize: 14,
    color: COLORS.textSecondary,
  },
  conversionNote: {
    flexDirection: 'row',
    alignItems: 'flex-start',
    backgroundColor: COLORS.light,
    padding: 12,
    borderRadius: 10,
    marginTop: 6,
    gap: 8,
  },
  conversionNoteText: {
    fontSize: 12,
    color: COLORS.dark,
    flex: 1,
    lineHeight: 17,
  },

  // How It Works
  stepsCard: {
    backgroundColor: COLORS.white,
    borderRadius: 16,
    padding: 20,
    marginBottom: 24,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 1 },
    shadowOpacity: 0.04,
    shadowRadius: 3,
    elevation: 1,
  },
  stepRow: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 16,
  },
  stepNum: {
    width: 32,
    height: 32,
    borderRadius: 16,
    backgroundColor: COLORS.primary,
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: 14,
  },
  stepNumText: {
    fontSize: 15,
    fontWeight: '800',
    color: COLORS.text,
  },
  stepInfo: {
    flex: 1,
  },
  stepTitle: {
    fontSize: 14,
    fontWeight: '700',
    color: COLORS.text,
    marginBottom: 2,
  },
  stepDesc: {
    fontSize: 13,
    color: COLORS.textSecondary,
  },

  // CTAs
  ctaButton: {
    backgroundColor: COLORS.primary,
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    borderRadius: 14,
    paddingVertical: 16,
    marginBottom: 10,
    gap: 8,
    shadowColor: COLORS.dark,
    shadowOffset: { width: 0, height: 3 },
    shadowOpacity: 0.2,
    shadowRadius: 6,
    elevation: 4,
  },
  ctaText: {
    fontSize: 16,
    fontWeight: '800',
    color: '#000',
  },
  ctaSecondary: {
    backgroundColor: COLORS.white,
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    borderRadius: 14,
    paddingVertical: 16,
    marginBottom: 24,
    gap: 8,
    borderWidth: 1.5,
    borderColor: COLORS.primary,
  },
  ctaSecondaryText: {
    fontSize: 15,
    fontWeight: '700',
    color: COLORS.dark,
  },
});

export default TradeAndEarnScreen;
