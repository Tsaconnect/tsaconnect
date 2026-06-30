// components/services/ServicePricingSheet.tsx
//
// Buyer-facing "Pricing services — View / PayNow" sheet (spec §6.3). Opens only
// once the one-time contact fee is paid (gated by the caller). Resolves the
// service's category serviceFeeMode and shows the right charging model:
//
//   STANDARD — merchant service price (if > 0) + a per-area "Call-out fee".
//     The buyer may declare "Provider is in my residential area" to zero the
//     call-out (tier = 'residential'); otherwise the detected zone's call-out
//     applies. Total = price + call-out.
//
//   BOOKING  — admin-set merged call-out+booking fare; the buyer picks/confirms
//     their area tier and that fare is the SOLE charge (no service price).
//
// "Add to cart" stashes the resolved tier + quoted price/fee in the shared
// cart item's notes (see serviceCart.ts) so checkout can build the serviceTiers
// map and label the line "Call-out fee". "Pay now" adds then routes to checkout.
// Built entirely on the TSA Signature primitives (@/theme + @/components/ui).
import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { View } from 'react-native';
import { useRouter } from 'expo-router';
import { useTheme } from '@/theme';
import {
  Button,
  Divider,
  RadioGroup,
  Sheet,
  Spinner,
  SummaryCard,
  SummaryRow,
  Text,
  Toggle,
  useToast,
} from '@/components/ui';
import { api, type Product } from './api';
import { cartService } from './cart';
import { getShippingEstimate } from '../../services/orderApi';
import {
  getCategoryBookingFees,
  BOOKING_TIERS,
  BOOKING_TIER_LABELS,
  type BookingTier,
} from '../../services/bookingFeeApi';
import { encodeServiceNotes } from './serviceCart';

type FeeMode = 'standard' | 'booking';

interface ServicePricingSheetProps {
  visible: boolean;
  onClose: () => void;
  serviceId: string;
  // Buyer's residence, used to detect the standard call-out zone / default the
  // booking tier. Pass empty strings if unknown — the sheet copes gracefully.
  buyerCity?: string;
  buyerState?: string;
  buyerCountry?: string;
}

const fmtUsd = (n: number) => `$${n.toFixed(2)}`;

export const ServicePricingSheet: React.FC<ServicePricingSheetProps> = ({
  visible,
  onClose,
  serviceId,
  buyerCity = '',
  buyerState = '',
  buyerCountry = '',
}) => {
  const { spacing } = useTheme();
  const router = useRouter();
  const toast = useToast();

  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [product, setProduct] = useState<Product | null>(null);
  const [mode, setMode] = useState<FeeMode>('standard');
  const [categoryId, setCategoryId] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);

  // STANDARD state
  const [residential, setResidential] = useState(false);
  const [detectedZone, setDetectedZone] = useState<string>('same_country');
  const [detectedCallout, setDetectedCallout] = useState<number>(0);

  // BOOKING state
  const [bookingFees, setBookingFees] = useState<Partial<Record<BookingTier, number>>>({});
  const [bookingTier, setBookingTier] = useState<BookingTier>('same_country');

  const resolveCategoryId = (cat: Product['category']): string | null => {
    if (!cat) return null;
    return typeof cat === 'string' ? cat : cat.id;
  };

  // Map the coarse shipping zone the backend reports onto a booking tier
  // default (the finer residential/LGA tiers are buyer-declared).
  const zoneToBookingTier = (zone: string): BookingTier => {
    switch (zone) {
      case 'same_city':
        return 'same_city';
      case 'same_state':
        return 'same_state';
      case 'same_country':
        return 'same_country';
      case 'international':
        return 'outside_country';
      default:
        return (BOOKING_TIERS as readonly string[]).includes(zone)
          ? (zone as BookingTier)
          : 'same_country';
    }
  };

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const res = await api.getProductById(serviceId);
      if (!res.success || !res.data) {
        throw new Error(res.message || 'Failed to load service');
      }
      const svc = res.data;
      setProduct(svc);
      const catId = resolveCategoryId(svc.category);
      setCategoryId(catId);

      // Determine the category's serviceFeeMode. The product payload may embed
      // it on the category object; otherwise look it up among service/both
      // categories by id. Defaults to 'standard'.
      let feeMode: FeeMode = 'standard';
      const embedded =
        typeof svc.category === 'object'
          ? (svc.category as { serviceFeeMode?: FeeMode }).serviceFeeMode
          : undefined;
      if (embedded === 'booking' || embedded === 'standard') {
        feeMode = embedded;
      } else if (catId) {
        const cats = await api.getCategories({});
        if (cats.success && Array.isArray(cats.data)) {
          const match = cats.data.find((c) => c.id === catId || c._id === catId);
          if (match?.serviceFeeMode === 'booking') feeMode = 'booking';
        }
      }
      setMode(feeMode);

      if (feeMode === 'booking' && catId) {
        const feeRes = await getCategoryBookingFees(catId);
        if (feeRes.success && feeRes.data?.fees) {
          setBookingFees(feeRes.data.fees);
        }
      }

      // Detect the standard call-out zone + amount for the buyer's location.
      // Reuses the existing per-product shipping estimate (frozen API).
      if (feeMode === 'standard' && buyerCountry && buyerState) {
        const est = await getShippingEstimate(
          serviceId,
          buyerCity,
          buyerState,
          buyerCountry,
        );
        if (est.success && est.data) {
          setDetectedZone(est.data.zone || 'same_country');
          setDetectedCallout(est.data.shippingCost || 0);
        }
      } else if (feeMode === 'booking') {
        // default booking tier from the buyer's coarse location; detectedZone
        // drives which tiers are offered (allowedBookingTiers).
        if (buyerCountry && buyerState) {
          const est = await getShippingEstimate(serviceId, buyerCity, buyerState, buyerCountry);
          if (est.success && est.data?.zone) {
            setDetectedZone(est.data.zone);
            setBookingTier(zoneToBookingTier(est.data.zone));
          }
        }
      }
    } catch (e: any) {
      setError(e.message || 'Failed to load pricing');
    } finally {
      setLoading(false);
    }
  }, [serviceId, buyerCity, buyerState, buyerCountry]);

  useEffect(() => {
    if (visible) load();
  }, [visible, load]);

  const servicePrice = product?.price && product.price > 0 ? product.price : 0;

  // The server (resolveServiceTier) only honors a "residential" refinement when
  // the detected zone is same_city. Mirror that here so the quoted call-out can
  // never differ from what the backend actually charges (quote == charge).
  const residentialAllowed = detectedZone === 'same_city';

  // Resolved charge + the tier we send to the backend.
  const { calloutFee, total, resolvedTier } = useMemo(() => {
    if (mode === 'booking') {
      const fare = bookingFees[bookingTier] || 0;
      return { calloutFee: fare, total: fare, resolvedTier: bookingTier as BookingTier };
    }
    const effectiveResidential = residential && residentialAllowed;
    const fee = effectiveResidential ? 0 : detectedCallout;
    const tier: BookingTier = effectiveResidential
      ? 'residential'
      : zoneToBookingTier(detectedZone);
    return { calloutFee: fee, total: servicePrice + fee, resolvedTier: tier };
  }, [mode, bookingFees, bookingTier, residential, residentialAllowed, detectedCallout, detectedZone, servicePrice]);

  const canPay = total > 0;

  const addToCart = useCallback(async (): Promise<boolean> => {
    if (!product) return false;
    setAdding(true);
    try {
      const productId = product.id || product._id || serviceId;
      const notes = encodeServiceNotes({
        tier: resolvedTier,
        price: mode === 'booking' ? 0 : servicePrice,
        fee: calloutFee,
        mode,
      });
      const res = await cartService.addToCart({ productId, quantity: 1, notes });
      if (!res.success) {
        toast.show(res.message || 'Failed to add to cart', 'error');
        return false;
      }
      return true;
    } catch (e: any) {
      toast.show(e.message || 'Failed to add to cart', 'error');
      return false;
    } finally {
      setAdding(false);
    }
  }, [product, serviceId, resolvedTier, mode, servicePrice, calloutFee, toast]);

  const handleAddToCart = async () => {
    const ok = await addToCart();
    if (ok) {
      toast.show('Service added to cart', 'success');
      onClose();
    }
  };

  const handlePayNow = async () => {
    const ok = await addToCart();
    if (ok) {
      onClose();
      router.push('/checkout');
    }
  };

  // Only offer the tiers the backend will actually honor for this buyer: the
  // detected coarse tier, plus a "residential" refinement when in the same city
  // and "same_lga" when in the same city/state. Picking a farther/closer tier the
  // server would override is hidden, so the quoted fare always equals the charge.
  const allowedBookingTiers = useMemo(() => {
    const allowed: BookingTier[] = [];
    if (detectedZone === 'same_city') allowed.push('residential');
    if (detectedZone === 'same_city' || detectedZone === 'same_state') allowed.push('same_lga');
    allowed.push(zoneToBookingTier(detectedZone));
    return BOOKING_TIERS.filter((t) => allowed.includes(t));
  }, [detectedZone]);

  const bookingOptions = useMemo(
    () =>
      allowedBookingTiers.map((t) => ({
        value: t,
        label: BOOKING_TIER_LABELS[t],
        sublabel: fmtUsd(bookingFees[t] || 0),
      })),
    [allowedBookingTiers, bookingFees],
  );

  return (
    <Sheet visible={visible} onClose={onClose} title="Service Pricing">
      <View style={{ paddingHorizontal: spacing.lg, paddingTop: spacing.md, paddingBottom: spacing.md, gap: spacing.lg }}>
        {loading ? (
          <View style={{ paddingVertical: spacing['2xl'], alignItems: 'center' }}>
            <Spinner label="Loading pricing…" />
          </View>
        ) : error ? (
          <View style={{ gap: spacing.md }}>
            <Text variant="bodySm" color="danger">
              {error}
            </Text>
            <Button title="Retry" variant="secondary" onPress={load} />
          </View>
        ) : mode === 'booking' ? (
          <>
            <Text variant="bodySm" color="textSecondary">
              This is a booking service. Confirm your area — the fare below is the
              full charge.
            </Text>
            <RadioGroup
              options={bookingOptions}
              value={bookingTier}
              onChange={(v) => setBookingTier(v as BookingTier)}
            />
            <SummaryCard dividerBeforeLast>
              <SummaryRow label="Booking fare" value={fmtUsd(calloutFee)} />
              <SummaryRow label="Total" value={fmtUsd(total)} total valueColor="accent" />
            </SummaryCard>
          </>
        ) : (
          <>
            {servicePrice > 0 ? (
              <Text variant="bodySm" color="textSecondary">
                Pay the service price plus a call-out fee for your area.
              </Text>
            ) : (
              <Text variant="bodySm" color="textSecondary">
                This provider charges a call-out fee for your area (price quoted
                per job).
              </Text>
            )}

            {/* Residential declaration — only offered when the provider is in
                the buyer's city (the only case the backend treats as free). */}
            {residentialAllowed && (
              <View
                style={{
                  flexDirection: 'row',
                  alignItems: 'center',
                  gap: spacing.md,
                }}
              >
                <View style={{ flex: 1 }}>
                  <Text variant="bodyMed" weight="600">
                    Provider is in my residential area
                  </Text>
                  <Text variant="caption" color="textSecondary">
                    Call-out free if you live in the same immediate area.
                  </Text>
                </View>
                <Toggle
                  value={residential}
                  onValueChange={setResidential}
                  accessibilityLabel="Provider is in my residential area, call-out free"
                />
              </View>
            )}

            <Divider />

            <SummaryCard dividerBeforeLast>
              {servicePrice > 0 && (
                <SummaryRow label="Service price" value={fmtUsd(servicePrice)} />
              )}
              <SummaryRow label="Call-out fee" value={calloutFee > 0 ? fmtUsd(calloutFee) : 'Free'} />
              <SummaryRow label="Total" value={fmtUsd(total)} total valueColor="accent" />
            </SummaryCard>
          </>
        )}

        {!loading && !error && (
          <View style={{ gap: spacing.sm }}>
            {!canPay && (
              <Text variant="caption" color="textMuted" align="center">
                No charge for this selection — nothing to pay.
              </Text>
            )}
            <Button
              title="Pay now"
              size="lg"
              leftIcon="shield"
              onPress={handlePayNow}
              loading={adding}
              disabled={adding || !canPay}
            />
            <Button
              title="Add to cart"
              variant="secondary"
              onPress={handleAddToCart}
              loading={adding}
              disabled={adding || !canPay}
            />
          </View>
        )}
      </View>
    </Sheet>
  );
};

export default ServicePricingSheet;
