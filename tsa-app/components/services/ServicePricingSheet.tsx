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
//
// Screen-fit layout: ALL content (slot/menu pickers, party size, policies,
// summaries) lives in the Sheet's scrollable body; the flow's primary CTA is
// pinned in the Sheet footer so it stays visible on short screens. The pickers
// run in hideSubmit mode and mirror their selection up for the footer CTA.
// Built entirely on the TSA Signature primitives (@/theme + @/components/ui).
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { View } from 'react-native';
import { useRouter } from 'expo-router';
import { useTheme } from '@/theme';
import { formatMicroPrice, isMicroPrice } from '@/utils/formatPrice';
import {
  Button,
  CounterControl,
  Divider,
  InlineMessage,
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
import { getShippingEstimate, quoteServiceOrder, type ServiceQuote } from '../../services/orderApi';
import {
  getCategoryBookingFees,
  BOOKING_TIERS,
  BOOKING_TIER_LABELS,
  type BookingTier,
} from '../../services/bookingFeeApi';
import { encodeServiceNotes } from './serviceCart';
import {
  getServiceProfile,
  getBookingFormSchema,
  type ServiceProfile,
  type BookingField,
  type BookingType,
} from '../../services/serviceListingApi';
import { runServiceCheckout } from '../../services/serviceCheckout';
import { createBookingRequest } from '../../services/bookingRequestApi';
import MenuPicker from './MenuPicker';
import SlotPicker from './SlotPicker';
import BookingForm, { type BookingFormHandle } from './BookingForm';
import { priceUnitSuffix, quantityCount, quantityLabel, stayUnitNoun } from '@/utils/priceUnit';
import { bookingQuoteRefusal } from '@/utils/bookingQuoteRefusal';

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

const fmtUsd = (n: number) => (isMicroPrice(n) ? `$${formatMicroPrice(n)}` : `$${n.toFixed(2)}`);

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
  // Auto-generated booking-form fields (unified booking engine). When non-empty
  // the sheet renders a schema-driven BookingForm instead of the legacy flow.
  const [customFields, setCustomFields] = useState<BookingField[]>([]);
  // Booking workflow: 'instant' escrows immediately; others create a request the
  // merchant must approve before the buyer pays (Phase 2).
  const [bookingType, setBookingType] = useState<BookingType>('instant');
  // Set when the category books by quantity (seats, tickets): the buyer picks
  // how many, and perUnit says the price is paid for each one.
  const [quantitySpec, setQuantitySpec] = useState<{ max: number; perUnit: boolean; unit?: string } | null>(null);
  const [quantity, setQuantity] = useState(1);
  // The booking form's answers as the buyer fills them, and what they cost
  // (the server's quote; null while not priceable yet).
  const [formAnswers, setFormAnswers] = useState<Record<string, any>>({});
  const [quote, setQuote] = useState<ServiceQuote | null>(null);
  // Why a filled-in booking cannot be priced (sold out, too few places left).
  const [quoteRefusal, setQuoteRefusal] = useState<string | null>(null);
  // Set when the category books stays: the form asks for the dates, and the
  // price is per night/day/month of the stay unless it is a fixed total.
  const [stayPricing, setStayPricing] = useState<{ perUnit: boolean; unit?: string } | null>(null);
  const [categoryId, setCategoryId] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);
  // Inline escrow checkout state for the order/booking/access-fee flows.
  const [checkingOut, setCheckingOut] = useState(false);
  // Why the last cart / checkout / booking attempt failed; shown above the pinned CTA.
  const [actionError, setActionError] = useState<string | null>(null);

  // Selections mirrored up from the hideSubmit pickers so the pinned footer
  // CTA can enable/disable and check out.
  const [menuSelections, setMenuSelections] = useState<{ itemId: string; quantity: number }[]>([]);
  const [slotSelection, setSlotSelection] = useState<{ slotStart: string; partySize: number } | null>(null);
  const bookingFormRef = useRef<BookingFormHandle>(null);

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
    setMenuSelections([]);
    setSlotSelection(null);
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

      // Auto-generated booking-form schema. Fields present → schema-driven form;
      // empty (or fetch failure) → the legacy per-serviceType flow below.
      const schemaRes = await getBookingFormSchema(serviceId);
      setCustomFields(schemaRes.success && schemaRes.data ? schemaRes.data.fields || [] : []);
      setBookingType(schemaRes.success && schemaRes.data ? schemaRes.data.bookingType : 'instant');
      const schema = schemaRes.success ? schemaRes.data : undefined;
      setQuantitySpec(
        schema?.serviceType === 'booking' && schema.bookingMode === 'quantity'
          ? { max: schema.maxQuantity || 1, perUnit: !!schema.chargesPerUnit, unit: schema.priceUnit }
          : null,
      );
      setQuantity(1);
      setFormAnswers({});
      setQuote(null);
      setStayPricing(
        schema?.serviceType === 'booking' && schema.bookingMode === 'date_range'
          ? { perUnit: !!schema.chargesPerUnit, unit: schema.priceUnit }
          : null,
      );

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
    else setActionError(null);
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
      const units = quantitySpec?.perUnit ? quantity : 1;
      return { calloutFee: fare, total: fare * units, resolvedTier: bookingTier as BookingTier };
    }
    const effectiveResidential = residential && residentialAllowed;
    const fee = effectiveResidential ? 0 : detectedCallout;
    const tier: BookingTier = effectiveResidential
      ? 'residential'
      : zoneToBookingTier(detectedZone);
    return { calloutFee: fee, total: servicePrice + fee, resolvedTier: tier };
  }, [mode, bookingFees, bookingTier, quantitySpec, quantity, residential, residentialAllowed, detectedCallout, detectedZone, servicePrice]);

  const canPay = total > 0;

  const addToCart = useCallback(async (): Promise<boolean> => {
    if (!product) return false;
    setAdding(true);
    setActionError(null);
    try {
      const productId = product.id || product._id || serviceId;
      const notes = encodeServiceNotes({
        tier: resolvedTier,
        price: mode === 'booking' ? 0 : servicePrice,
        fee: calloutFee,
        mode,
      });
      const res = await cartService.addToCart({ productId, quantity: quantitySpec ? quantity : 1, notes });
      if (!res.success) {
        setActionError(res.message || 'Could not add this service to your cart. Try again.');
        return false;
      }
      return true;
    } catch (e: any) {
      setActionError(e.message || 'Could not add this service to your cart. Try again.');
      return false;
    } finally {
      setAdding(false);
    }
  }, [product, serviceId, resolvedTier, mode, servicePrice, calloutFee, quantitySpec, quantity]);

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
      answers?: Record<string, any>;
    }) => {
      setCheckingOut(true);
      setActionError(null);
      try {
        await runServiceCheckout({
          token: 'USDC',
          productId: productIdOf(),
          buyerCity,
          buyerState,
          buyerCountry,
          menuSelections: extra.menuSelections,
          bookingSlot: extra.bookingSlot,
          quantity: quantitySpec ? quantity : undefined,
          answers: extra.answers,
        });
        toast.show('Order placed & funded via escrow', 'success');
        onClose();
        router.push('/(dashboard)/orderlist');
      } catch (e: any) {
        setActionError(e.message || 'Checkout did not go through. Try again.');
      } finally {
        setCheckingOut(false);
      }
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [buyerCity, buyerState, buyerCountry, quantitySpec, quantity, toast, onClose, router],
  );

  // BookingForm submit — instant bookings escrow immediately; request/approval
  // (and quote) bookings create a request the merchant must approve first.
  const handleBookingSubmit = useCallback(
    async (answers: Record<string, any>) => {
      if (bookingType === 'instant') {
        // A slotted listing books a time from the slot picker shown above its form.
        const slotted = (profile?.slotDurationMinutes ?? 0) > 0;
        if (slotted && !slotSelection) {
          toast.show('Pick a time slot first', 'error');
          return;
        }
        await runInlineCheckout({ answers, bookingSlot: slotted ? slotSelection ?? undefined : undefined });
        return;
      }
      setCheckingOut(true);
      setActionError(null);
      try {
        const res = await createBookingRequest(productIdOf(), {
          token: 'USDC',
          buyerCity,
          buyerState,
          buyerCountry,
          quantity: quantitySpec ? quantity : undefined,
          answers,
        });
        if (!res.success) {
          setActionError(res.message || 'Could not send the booking request. Try again.');
          return;
        }
        toast.show('Booking request sent — awaiting provider approval', 'success');
        onClose();
        // Simplified path (route groups are transparent). Cast: typed routes
        // regenerate on the next `expo start` and don't yet include this screen.
        router.push('/booking-requests' as any);
      } catch (e: any) {
        setActionError(e.message || 'Could not send the booking request. Try again.');
      } finally {
        setCheckingOut(false);
      }
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [bookingType, profile, slotSelection, buyerCity, buyerState, buyerCountry, quantitySpec, quantity, toast, onClose, router, runInlineCheckout],
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

  // A booking service with a configured FieldConfig renders the schema-driven
  // BookingForm (which owns its own submit button) instead of the legacy flow.
  const usesBookingForm = serviceType === 'booking' && customFields.length > 0;
  // A slotted listing with a form books a time AND answers the form.
  const slottedForm = usesBookingForm && (profile?.slotDurationMinutes ?? 0) > 0;

  // Price the booking as it is filled in — the server's own pricing, rolled
  // back — so the buyer sees "$50.00 × 3 nights" before paying.
  useEffect(() => {
    if (!visible || loading || !usesBookingForm) {
      setQuote(null);
      setQuoteRefusal(null);
      return;
    }
    let live = true;
    const timer = setTimeout(async () => {
      const res = await quoteServiceOrder({
        token: 'USDC',
        productId: productIdOf(),
        buyerCity,
        buyerState,
        buyerCountry,
        quantity: quantitySpec ? quantity : undefined,
        bookingSlot: slottedForm ? slotSelection ?? undefined : undefined,
        answers: formAnswers,
      });
      if (!live) return;
      setQuote(res.success && res.data ? res.data : null);
      setQuoteRefusal(bookingQuoteRefusal(res));
    }, 350);
    return () => {
      live = false;
      clearTimeout(timer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [visible, loading, usesBookingForm, slottedForm, slotSelection, quantitySpec, quantity, formAnswers, buyerCity, buyerState, buyerCountry]);

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

  // Pinned footer CTA per flow — always visible below the scrollable body.
  // Null while loading/errored (the Retry button lives in the body). The
  // branch order mirrors the body's flow branches below.
  const footer: React.ReactNode =
    loading || error ? null : usesBookingForm ? (
      <Button
        title={bookingType === 'instant' ? 'Book & Pay' : 'Request booking'}
        size="lg"
        leftIcon="shield"
        onPress={() => bookingFormRef.current?.submit()}
        loading={checkingOut}
        disabled={checkingOut}
      />
    ) : serviceType === 'order' ? (
      <Button
        title="Pay now"
        size="lg"
        leftIcon="shield"
        onPress={() => handleMenuCheckout(menuSelections)}
        loading={checkingOut}
        disabled={checkingOut || menuSelections.length === 0}
      />
    ) : serviceType === 'booking' && !isFareOnlyBooking ? (
      <Button
        title="Pay now"
        size="lg"
        leftIcon="shield"
        onPress={() => slotSelection && handleSlotCheckout(slotSelection)}
        loading={checkingOut}
        disabled={checkingOut || !slotSelection}
      />
    ) : isFareOnlyBooking ? (
      <Button
        title="Pay now"
        size="lg"
        leftIcon="shield"
        onPress={handleAccessFeeCheckout}
        loading={checkingOut}
        disabled={checkingOut}
      />
    ) : (
      <>
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
      </>
    );

  return (
    <Sheet
      visible={visible}
      onClose={onClose}
      title="Service Pricing"
      scrollable
      footer={footer ? (
        <>
          <InlineMessage message={actionError} />
          {footer}
        </>
      ) : null}
    >
      <View style={{ paddingHorizontal: spacing.lg, paddingTop: spacing.md, paddingBottom: spacing.md, gap: spacing.lg }}>
        {!loading && !error && quantitySpec && (
          <View style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' }}>
            <View style={{ flex: 1 }}>
              <Text variant="bodyMed" weight="600">
                {quantityLabel(quantitySpec.unit)}
              </Text>
              <Text variant="caption" color="textSecondary">
                {quantitySpec.perUnit ? 'The price is for each one.' : 'One price for the whole booking.'}
              </Text>
            </View>
            <CounterControl
              value={quantity}
              onChange={setQuantity}
              label={quantityLabel(quantitySpec.unit).toLowerCase()}
              min={1}
              max={quantitySpec.max}
            />
          </View>
        )}
        {!loading && !error && stayPricing && (
          <Text variant="bodySm" color="textSecondary">
            {stayPricing.perUnit
              ? `Pick your dates. The price is per ${stayUnitNoun(stayPricing.unit)} of your stay.`
              : 'Pick your dates. The price covers the whole stay.'}
          </Text>
        )}
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
        ) : usesBookingForm ? (
          /* BOOKING (configurable form) — schema-driven fields, from the listing
             or its category; checks out with the collected answers. A slotted
             listing also shows its slot picker (the backend requires a slot
             whenever slotDurationMinutes > 0). The live quote follows. */
          <>
            {slottedForm ? (
              <SlotPicker
                productId={productIdOf()}
                hideSubmit
                onSelectionChange={setSlotSelection}
                partySize={quantitySpec ? quantity : undefined}
              />
            ) : null}
            <BookingForm
              ref={bookingFormRef}
              fields={customFields}
              hideSubmit
              onSubmit={handleBookingSubmit}
              onChange={setFormAnswers}
            />
            {quote ? <BookingQuote quote={quote} /> : <InlineMessage tone="warning" message={quoteRefusal} />}
          </>
        ) : serviceType === 'order' ? (
          /* ORDER — menu picker; the pinned footer checks out menuSelections. */
          <MenuPicker
            productId={productIdOf()}
            hideSubmit
            onSelectionChange={setMenuSelections}
          />
        ) : serviceType === 'booking' && !isFareOnlyBooking ? (
          /* BOOKING — slot picker + policies; the pinned footer checks out the
             mirrored slotSelection. */
          <>
            <SlotPicker
              productId={productIdOf()}
              hideSubmit
              onSelectionChange={setSlotSelection}
              partySize={quantitySpec ? quantity : undefined}
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
              <SummaryRow
                label={quantitySpec?.perUnit && quantity > 1 ? `Booking fare × ${quantity}` : 'Booking fare'}
                value={`${fmtUsd(calloutFee)}${quantitySpec?.perUnit ? priceUnitSuffix(quantitySpec.unit) : ''}`}
              />
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
      </View>
    </Sheet>
  );
};

export default ServicePricingSheet;

/** What the booking costs as filled in: "$50.00 × 3 nights", fees, total. */
function BookingQuote({ quote }: { quote: ServiceQuote }) {
  if (quote.onRequest) {
    return (
      <Text variant="bodySm" color="textSecondary">
        The provider names the price when they accept your request.
      </Text>
    );
  }
  const units = quote.units;
  const unitCount =
    quote.bookingMode === 'date_range'
      ? `${units} ${stayUnitNoun(quote.priceUnit)}${units === 1 ? '' : 's'}`
      : quantityCount(quote.priceUnit, units);
  const perUnit = quote.chargesPerUnit && quote.unitPriceUsd ? quote.unitPriceUsd : 0;
  const extra = quote.priceUsd - perUnit * units; // an access fee on top of the units
  return (
    <SummaryCard dividerBeforeLast>
      {perUnit ? (
        <SummaryRow label={`${fmtUsd(perUnit)} × ${unitCount}`} value={fmtUsd(perUnit * units)} />
      ) : (
        <SummaryRow label="Price" value={fmtUsd(quote.priceUsd)} />
      )}
      {perUnit && extra > 0.005 ? <SummaryRow label="Access fee" value={fmtUsd(extra)} /> : null}
      {quote.calloutUsd > 0 ? <SummaryRow label="Call-out fee" value={fmtUsd(quote.calloutUsd)} /> : null}
      <SummaryRow label="Total" value={fmtUsd(quote.totalUsd)} total valueColor="accent" />
    </SummaryCard>
  );
}
