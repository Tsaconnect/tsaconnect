// Buyer-side claim UI on a promoted listing's detail screen. Hidden for the
// listing's owner. The server prices checkout; this only shows where the buyer
// stands (unclaimed -> progress + countdown -> unlocked -> used | expired).
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { Alert, View } from 'react-native';
import { useFocusEffect } from 'expo-router';
import api, { Product, PromoStatus } from '@/components/services/api';
import { useAuth } from '@/AuthContext/AuthContext';
import { useTheme } from '@/theme';
import { Button, Card, Text } from '@/components/ui';
import { formatMmSs, isPromoted, promoPhase, remainingMs } from '@/utils/promo';
import { referralCodeOf, shareReferralCode } from '@/utils/referralShare';

const POLL_MS = 15000;

export interface PromoUnlockCardProps {
  product: Pick<
    Product,
    | 'id'
    | 'userId'
    | 'price'
    | 'promoEnabled'
    | 'promoPrice'
    | 'promoDiscountPercent'
    | 'promoRequiredReferrals'
    | 'promoCountdownSeconds'
  >;
  format: (usd: number) => string;
  /** Lets the screen show the right price (promo only once unlocked). */
  onStatusChange?: (status: PromoStatus | null) => void;
}

export const PromoUnlockCard: React.FC<PromoUnlockCardProps> = ({ product, format, onStatusChange }) => {
  const { colors, spacing, radius } = useTheme();
  const { currentUser } = useAuth();
  const [status, setStatus] = useState<PromoStatus | null>(null);
  const [claiming, setClaiming] = useState(false);
  const [now, setNow] = useState(() => Date.now());
  const onStatusRef = useRef(onStatusChange);
  onStatusRef.current = onStatusChange;

  const viewerId = currentUser?._id ?? currentUser?.id;
  const isOwner = !!viewerId && !!product.userId && String(viewerId) === String(product.userId);
  const active = isPromoted(product) && !isOwner;

  const apply = useCallback((next: PromoStatus | null) => {
    setStatus(next);
    onStatusRef.current?.(next);
  }, []);

  const refresh = useCallback(async () => {
    const res = await api.getPromoStatus(product.id);
    if (res.success && res.data) apply(res.data);
  }, [product.id, apply]);

  const statusRef = useRef<PromoStatus | null>(null);
  statusRef.current = status;

  const phase = promoPhase(status);

  // Status on focus, then every ~15s while the buyer is waiting on referrals.
  useFocusEffect(
    useCallback(() => {
      if (!active) return;
      refresh();
      const timer = setInterval(() => {
        if (promoPhase(statusRef.current) === 'progress') refresh();
      }, POLL_MS);
      return () => clearInterval(timer);
    }, [active, refresh]),
  );

  const hasCountdown = phase === 'progress' && !!status?.expiresAt;
  useEffect(() => {
    if (!hasCountdown) return;
    const tick = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(tick);
  }, [hasCountdown]);

  const left = remainingMs(status?.expiresAt, now);
  const timedOut = hasCountdown && left <= 0;
  useEffect(() => {
    if (timedOut) refresh();
  }, [timedOut, refresh]);

  if (!active) return null;

  const claim = async () => {
    setClaiming(true);
    try {
      const res = await api.claimPromo(product.id);
      if (res.success && res.data) {
        setNow(Date.now());
        apply(res.data);
      } else {
        Alert.alert('Promotion', res.message || 'Could not start the promotion. Please try again.');
      }
    } finally {
      setClaiming(false);
    }
  };

  const share = async () => {
    try {
      let code = referralCodeOf(currentUser);
      if (!code) {
        const profile = await api.getProfile();
        code = referralCodeOf(profile.data);
      }
      if (!code) {
        Alert.alert('Referral', 'No referral code available');
        return;
      }
      await shareReferralCode(code);
    } catch {
      Alert.alert('Error', 'Failed to share referral code');
    }
  };

  const required = status?.required ?? product.promoRequiredReferrals ?? 0;
  const current = Math.min(status?.current ?? 0, required);
  const pct = required > 0 ? Math.round((current / required) * 100) : 0;

  return (
    <Card padding="md" style={{ gap: spacing.sm, marginTop: spacing.md }}>
      {phase === 'unclaimed' && (
        <>
          <Text variant="bodyMed" weight="600">
            {`Promotion: bring ${product.promoRequiredReferrals ?? 0} verified referrals to unlock ${product.promoDiscountPercent}% off`}
          </Text>
          <Button title="Unlock discount" onPress={claim} loading={claiming} disabled={claiming} />
        </>
      )}

      {phase === 'progress' && (
        <>
          <Text variant="bodyMed" weight="600">{`${current} / ${required} verified referrals`}</Text>
          <View
            accessibilityRole="progressbar"
            accessibilityLabel="Verified referrals"
            accessibilityValue={{ min: 0, max: required, now: current }}
            style={{ height: 8, borderRadius: radius.pill, backgroundColor: colors.surfaceAlt, overflow: 'hidden' }}
          >
            <View style={{ width: `${pct}%`, height: '100%', backgroundColor: colors.accent }} />
          </View>
          {hasCountdown && (
            <Text variant="bodySm" color="textSecondary" numeric accessibilityLiveRegion="polite">
              {`Time left ${formatMmSs(left)}`}
            </Text>
          )}
          <Button title="Share my referral link" variant="secondary" leftIcon="share" onPress={share} />
        </>
      )}

      {phase === 'unlocked' && (
        <>
          <Text variant="bodyMed" weight="600" color="success">
            {`Discount unlocked - you pay ${format(product.promoPrice ?? product.price)}`}
          </Text>
          <Text variant="bodySm" color="textSecondary">
            Applies to one item per unlock
          </Text>
        </>
      )}

      {phase === 'used' && (
        <>
          <Text variant="bodyMed" weight="600">
            {`Discount used - it covers one item. Bring ${required} new verified referrals to earn it again.`}
          </Text>
          <Text variant="bodySm" color="textSecondary" numeric>
            {format(product.price)}
          </Text>
          <Button title="Start again" onPress={claim} loading={claiming} disabled={claiming} />
        </>
      )}

      {phase === 'expired' && (
        <>
          <Text variant="bodyMed" weight="600" color="danger">
            Time's up - this item is now full price
          </Text>
          <Text variant="bodySm" color="textSecondary" numeric>
            {format(product.price)}
          </Text>
        </>
      )}
    </Card>
  );
};

export default PromoUnlockCard;
