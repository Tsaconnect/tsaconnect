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
import { api, type Category, type Product } from './api';
import { cartService } from './cart';
import { getShippingEstimate } from '../../services/orderApi';
import {
  getCategoryBookingFees,
  BOOKING_TIERS,
  BOOKING_TIER_LABELS,
  type BookingTier,
} from '../../services/bookingFeeApi';
import { encodeServiceNotes } from './serviceCart';
import { getServiceProfile, type ServiceProfile } from '../../services/serviceListingApi';
import { runServiceCheckout } from '../../services/serviceCheckout';
import MenuPicker from './MenuPicker';
import SlotPicker from './SlotPicker';

type FeeMode = 'standard' | 'booking';
// The three marketplace service types drive which buyer flow the sheet shows.
// 'contact' keeps the existing standard/booking-fare (access-fee) flow; 'order'
// shows the menu picker; 'booking' shows the slot picker (fare-only when the
// profile has slotDurationMinutes == 0).
type ServiceType = 'contact' | 'order' | 'booking';

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
  const [serviceType, setServiceType] = useState<ServiceType>('contact');
  const [profile, setProfile] = useState<ServiceProfile | null>(null);
  const [categoryId, setCategoryId] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);
  // Inline escrow checkout state for the order/booking/access-fee flows.
  const [checkingOut, setCheckingOut] = useState(false);

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

      // Determine the category's serviceFeeMode + serviceType. Both may be
      // embedded on the product's category object; otherwise look the category
      // up by id. serviceFeeMode defaults to 'standard'; serviceType (which
      // branches the whole buyer flow) defaults to 'contact' when missing.
      let feeMode: FeeMode = 'standard';
      let svcType: ServiceType = 'contact';
      const embeddedCat =
        typeof svc.category === 'object'
          ? (svc.category as Partial<Category>)
          : undefined;
      if (embeddedCat?.serviceFeeMode === 'booking' || embeddedCat?.serviceFeeMode === 'standard') {
        feeMode = embeddedCat.serviceFeeMode;
      }
      if (
        embeddedCat?.serviceType === 'order' ||
        embeddedCat?.serviceType === 'booking' ||
        embeddedCat?.serviceType === 'contact'
      ) {
        svcType = embeddedCat.serviceType;
      }
      // Fall back to a category fetch when either field wasn't embedded.
      if ((!embeddedCat?.serviceFeeMode || !embeddedCat?.serviceType) && catId) {
        const cats = await api.getCategories({});
        if (cats.success && Array.isArray(cats.data)) {
          const match = cats.data.find((c) => c.id === catId || c._id === catId);
          if (match?.serviceFeeMode === 'booking') feeMode = 'booking';
          if (
            match?.serviceType === 'order' ||
            match?.serviceType === 'booking' ||
            match?.serviceType === 'contact'
          ) {
            svcType = match.serviceType;
          }
        }
      }
      setMode(feeMode);
      setServiceType(svcType);

      // The service profile carries the access/call-out fees, slot config, and
      // booking policies. Public read; missing profile is non-fatal (the flows
      // degrade gracefully — e.g. legacy fare-only booking).
      const profRes = await getServiceProfile(serviceId);
      const prof = profRes.success && profRes.data ? profRes.data : null;
      setProfile(prof);

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

  // Inline escrow checkout for the order / booking flows (and access-fee-only
  // bookings). These carry menuSelections / bookingSlots that the shared cart
  // route can't thread, so they run their own single-order escrow flow here.
  // Money is recomputed server-side; the app default token is USDC (same as the
  // cart checkout default). On success the order is escrowed and we route to the
  // buyer's order list.
  const productIdOf = () => product?.id || product?._id || serviceId;

  const runInlineCheckout = useCallback(
    async (extra: {
      menuSelections?: { itemId: string; quantity: number }[];
      bookingSlot?: { slotStart: string; partySize: number };
    }) => {
      setCheckingOut(true);
      try {
        await runServiceCheckout({
          token: 'USDC',
          productId: productIdOf(),
          buyerCity,
          buyerState,
          buyerCountry,
          menuSelections: extra.menuSelections,
          bookingSlot: extra.bookingSlot,
        });
        toast.show('Order placed & funded via escrow', 'success');
        onClose();
        router.push('/(dashboard)/orderlist');
      } catch (e: any) {
        toast.show(e.message || 'Checkout failed', 'error');
      } finally {
        setCheckingOut(false);
      }
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [buyerCity, buyerState, buyerCountry, toast, onClose, router],
  );

  const handleMenuCheckout = (menuItems: { itemId: string; quantity: number }[]) =>
    runInlineCheckout({ menuSelections: menuItems });

  const handleSlotCheckout = (slot: { slotStart: string; partySize: number }) =>
    runInlineCheckout({ bookingSlot: slot });

  // Access-fee-only checkout (contact flow after unlock, and legacy fare-only
  // booking): no menu/slot payload — the server charges the profile access/
  // call-out fee for this service. The declared area tier still rides the cart
  // path for the standard/booking-fare sheet below, so this is used only for the
  // simple access-fee confirmation buttons.
  const handleAccessFeeCheckout = () => runInlineCheckout({});

  // Legacy fare-only booking: profile has no slot duration → skip the slot grid
  // and charge the booking fare directly.
  const isFareOnlyBooking = serviceType === 'booking' && (profile?.slotDurationMinutes ?? 0) === 0;

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
        ) : serviceType === 'order' ? (
          /* ORDER — menu picker; checks out with menuSelections. */
          <MenuPicker
            productId={productIdOf()}
            onCheckout={handleMenuCheckout}
            submitting={checkingOut}
          />
        ) : serviceType === 'booking' && !isFareOnlyBooking ? (
          /* BOOKING — slot picker + policies; checks out with bookingSlots. */
          <>
            <SlotPicker
              productId={productIdOf()}
              onCheckout={handleSlotCheckout}
              submitting={checkingOut}
            />
            {(profile?.cancellationPolicy ||
              profile?.reschedulePolicy ||
              profile?.refundPolicy ||
              profile?.bookingRules) && (
              <View style={{ gap: spacing.sm }}>
                <Divider />
                <Text variant="bodyMed" weight="600">
                  Booking policies
                </Text>
                {profile?.cancellationPolicy ? (
                  <View>
                    <Text variant="caption" weight="600" color="textSecondary">
                      Cancellation
                    </Text>
                    <Text variant="bodySm" color="textSecondary">
                      {profile.cancellationPolicy}
                    </Text>
                  </View>
                ) : null}
                {profile?.reschedulePolicy ? (
                  <View>
                    <Text variant="caption" weight="600" color="textSecondary">
                      Reschedule
                    </Text>
                    <Text variant="bodySm" color="textSecondary">
                      {profile.reschedulePolicy}
                    </Text>
                  </View>
                ) : null}
                {profile?.refundPolicy ? (
                  <View>
                    <Text variant="caption" weight="600" color="textSecondary">
                      Refund
                    </Text>
                    <Text variant="bodySm" color="textSecondary">
                      {profile.refundPolicy}
                    </Text>
                  </View>
                ) : null}
                {profile?.bookingRules ? (
                  <View>
                    <Text variant="caption" weight="600" color="textSecondary">
                      Rules
                    </Text>
                    <Text variant="bodySm" color="textSecondary">
                      {profile.bookingRules}
                    </Text>
                  </View>
                ) : null}
              </View>
            )}
          </>
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
              {profile?.accessFeeUsd ? (
                <SummaryRow label="Access fee" value={fmtUsd(profile.accessFeeUsd)} />
              ) : null}
              {servicePrice > 0 && (
                <SummaryRow label="Service price" value={fmtUsd(servicePrice)} />
              )}
              <SummaryRow label="Call-out fee" value={calloutFee > 0 ? fmtUsd(calloutFee) : 'Free'} />
              <SummaryRow label="Total" value={fmtUsd(total)} total valueColor="accent" />
            </SummaryCard>
          </>
        )}

        {/* Order / non-fare booking render their own Pay button inside the
            picker — only the contact + legacy-fare paths use the shared footer. */}
        {!loading && !error && serviceType !== 'order' && !(serviceType === 'booking' && !isFareOnlyBooking) && (
          isFareOnlyBooking ? (
            <View style={{ gap: spacing.sm }}>
              <Button
                title="Pay now"
                size="lg"
                leftIcon="shield"
                onPress={handleAccessFeeCheckout}
                loading={checkingOut}
                disabled={checkingOut}
              />
            </View>
          ) : (
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
          )
        )}
      </View>
    </Sheet>
  );
};

export default ServicePricingSheet;
