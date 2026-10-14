// app/merchants/merchant-request.tsx
//
// Become-a-merchant request form. Restyled onto the TSA Signature design system
// (@/theme tokens + @/components/ui primitives). This is a visual/structural
// restyle ONLY — the existing-request fetch, AsyncStorage role write on approval,
// the required-field validation, the submitMerchantRequest payload (optional
// fields collapsed to `undefined`), the pending/approved/rejected status gating,
// the focus-chain refs, and every navigation route are all preserved verbatim.
//
// NOTE: the fields use a local token-themed TextInput `Field` (not the @/components/ui
// `Input`) because the focus-chain — businessName → … → submit, driven by the
// onSubmitEditing refs — must keep working, and the Input primitive does not yet
// forward a ref to its inner TextInput. `Field` mirrors Input's chrome exactly.
//
// When businessType === 'p2p_merchant', a "Payment Merchant setup" section is
// rendered that collects the six backend-required corporate PM fields:
//   settlementBankCode, settlementAccountNumber, settlementAccountName (resolved
//   via NUBAN name enquiry), makerWalletAddress, requestedPerTradeCapUsd,
//   requestedDailyCapUsd.
import React, { useState, useEffect, useRef, useMemo, useCallback } from 'react';
import {
  View,
  TextInput,
  TextInputProps,
  ScrollView,
  Alert,
  Keyboard,
  KeyboardAvoidingView,
  Linking,
  Platform,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { Stack, router, useFocusEffect, useLocalSearchParams } from 'expo-router';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { useAuth } from '../../AuthContext/AuthContext';
import api, { getMyMerchantRequest, submitMerchantRequest } from '../../components/services/api';
import { getNigerianBanks, NgBank, resolveAccountName } from '../../services/ngBanks';
import { getMyPmStatus } from '../../services/p2p';
import { useTheme } from '@/theme';
import { useKeyboardHeight } from '@/hooks/useKeyboardHeight';
import { COMPANY } from '@/constants/legal';
import { KYC_ROUTE, kycRefusalFor, promptKycRefusal } from '@/utils/kycRefusal';
import { LP_DASHBOARD_ROUTE } from '@/utils/lpCta';
import LocationPicker from '@/components/common/LocationPicker';
import { Button, Chip, Icon, PickerSheet, PressableScale, SelectRow, Spinner, Text } from '@/components/ui';
import type { PickerItem } from '@/components/ui';

const BUSINESS_TYPES = [
  { label: 'General Products', value: 'general_products' },
  { label: 'Digital Products', value: 'digital_products' },
  { label: 'Payment Merchant (PM)', value: 'p2p_merchant' },
  { label: 'Service Provider', value: 'service_provider' },
  { label: 'Accept payments (TSA Pay)', value: 'payment_acceptance' },
];

// ─── EVM address validation ──────────────────────────────────────────────────
const EVM_ADDRESS_RE = /^0x[0-9a-fA-F]{40}$/;
const ZERO_ADDRESS = '0x0000000000000000000000000000000000000000';

export function isValidEvmAddress(addr: string): boolean {
  return EVM_ADDRESS_RE.test(addr) && addr.toLowerCase() !== ZERO_ADDRESS;
}

// ─── PM field validation helper (exported for tests) ─────────────────────────
export interface PmFields {
  settlementBankCode: string;
  settlementAccountNumber: string;
  settlementAccountName: string;
  makerWalletAddress: string;
}

export interface PmValidationErrors {
  settlementBank?: string;
  settlementAccountNumber?: string;
  settlementAccountName?: string;
  makerWalletAddress?: string;
}

export function validatePmFields(fields: PmFields): PmValidationErrors {
  const errors: PmValidationErrors = {};
  if (!fields.settlementBankCode) {
    errors.settlementBank = 'Select a settlement bank';
  }
  if (!fields.settlementAccountNumber.trim()) {
    errors.settlementAccountNumber = 'Enter your settlement account number';
  }
  if (!fields.settlementAccountName.trim()) {
    errors.settlementAccountName = 'Account name is required — verify your account number';
  }
  if (!fields.makerWalletAddress.trim()) {
    errors.makerWalletAddress = 'Enter your maker wallet address';
  } else if (!isValidEvmAddress(fields.makerWalletAddress.trim())) {
    errors.makerWalletAddress = 'Enter a valid EVM address (0x… 40 hex chars, not zero address)';
  }
  return errors;
}

// ─── PM entry-point param helpers (exported for tests) ───────────────────────

/**
 * Returns the initial businessType state value from the URL `type` param.
 * When `type === 'p2p_merchant'` the form opens in locked PM mode; all other
 * values (including absent) default to `'general_products'`.
 */
const LOCKABLE_TYPES = ['p2p_merchant', 'payment_acceptance'];

export function initBusinessType(type: string | undefined): string {
  return type && LOCKABLE_TYPES.includes(type) ? type : 'general_products';
}

/**
 * Returns true when the business-type picker should be locked to a single
 * static chip (no `onPress`). This happens when the form was reached via a
 * `?type=p2p_merchant` or `?type=payment_acceptance` URL param — the user
 * explicitly chose that path (a deep link, e.g. from the merchant dashboard
 * or a "not now, something else" prompt), so we prevent switching away.
 */
export function isPickerLocked(type: string | undefined): boolean {
  return !!type && LOCKABLE_TYPES.includes(type);
}

/** Approved request types that grant the marketplace merchant role server-side. */
export function cachesMerchantRole(businessType?: string): boolean {
  return businessType !== 'payment_acceptance' && businessType !== 'liquidity_provider';
}

// ─── Existing-request gate (exported for tests) ──────────────────────────────
//
// Decides which screen to render given the caller's existing merchant request,
// whether the form is in PM-upgrade mode, and the user's ACTUAL Payment Merchant
// status (GET /p2p/pm/me).
//
// Why pmActive is needed: an approved p2p_merchant *request* is NOT the same as
// being an active Payment Merchant. Approval flips the request to "approved" but
// PM activation (Nomba sub-account + virtual account) is a separate step that can
// still be in progress or have failed. Trusting request.status alone produced a
// false "You're a Payment Merchant!" success screen for users with no active PM
// row — while the dashboard (which reads the real PM status) correctly still
// showed the "Upgrade" CTA. We gate the success screen on pmActive so the two
// surfaces agree, and surface a 'provisioning' state in the gap.

export interface ExistingRequestLike {
  status?: string;
  businessType?: string;
  business_type?: string;
  // Corporate-PM provisioning data (camelCase from the API, snake_case tolerated).
  // Their presence distinguishes a genuinely in-flight activation from a legacy
  // PM request approved before these fields existed (which carries none).
  makerWalletAddress?: string;
  maker_wallet_address?: string;
  settlementBankCode?: string;
  settlement_bank_code?: string;
}

// A PM request carries the data provisioning needs only if it was submitted
// through the corporate-PM form (settlement bank + maker wallet required there).
// Legacy p2p_merchant requests approved before those fields existed have neither —
// nothing will ever activate them, so they must be routed back to the form.
function hasPmProvisioningData(r: ExistingRequestLike): boolean {
  const maker = (r.makerWalletAddress ?? r.maker_wallet_address ?? '').trim();
  const bank = (r.settlementBankCode ?? r.settlement_bank_code ?? '').trim();
  return maker !== '' || bank !== '';
}

export type MerchantGate =
  | { screen: 'form' }
  | { screen: 'verify' }
  | { screen: 'pending'; pm: boolean }
  | { screen: 'approved'; pm: boolean; payFlow: boolean }
  | { screen: 'provisioning' }
  | { screen: 'rejected' }
  /** A request of the retired liquidity_provider kind: LPs now activate on /liquidity. */
  | { screen: 'liquidity' };

// Business types the backend has NO "already a merchant" role guard for —
// each may be applied for on top of whatever else the user already has
// (PayMerchantUpgrade's own carve-out for p2p_merchant; payment_acceptance's
// deliberate "no role guard — a marketplace merchant, or anyone else, may
// apply" in merchant_request_handler.go). Every OTHER business type keeps the
// original rule: in the normal flow, any existing request blocks a new one.
const ADD_ON_BUSINESS_TYPES = ['p2p_merchant', 'payment_acceptance'];

/**
 * The my-request ?type= filter for a form: an add-on form gates on a request of
 * its own type, so a newer request of another type (say an approved
 * p2p_merchant) never hides a pending TSA Pay application. Undefined = the newest
 * request of any type, as before.
 */
export function requestTypeFilter(formType: string): string | undefined {
  return ADD_ON_BUSINESS_TYPES.includes(formType) ? formType : undefined;
}

// A TSA Pay application needs a verified identity (the server refuses it with
// the money-in code, kyc_verification_required), so an unverified applicant is sent to verification instead of
// a form she could not submit. Only the form is replaced: a request she already
// has keeps its own status screen. kycVerified defaults to true because only a
// TSA Pay form ever asks.
export function resolveMerchantGate(args: {
  existingRequest: ExistingRequestLike | null | undefined;
  formType: string;
  pmActive: boolean;
  kycVerified?: boolean;
}): MerchantGate {
  const gate = resolveRequestGate(args);
  if (gate.screen === 'form' && args.formType === 'payment_acceptance' && args.kycVerified === false) {
    return { screen: 'verify' };
  }
  return gate;
}

function resolveRequestGate(args: {
  existingRequest: ExistingRequestLike | null | undefined;
  formType: string;
  pmActive: boolean;
}): MerchantGate {
  const { existingRequest, formType, pmActive } = args;
  if (!existingRequest) return { screen: 'form' };

  const existingType = existingRequest.businessType ?? existingRequest.business_type;
  const existingIsPm = existingType === 'p2p_merchant';
  // TSA Pay's payment_acceptance approval deliberately never grants
  // RoleMerchant (Task 4) — the marketplace-merchant "Go to Merchant
  // Dashboard" success screen is wrong for it and its API calls would 403.
  const existingIsPay = existingType === 'payment_acceptance';

  // Upgrade mode (?type=p2p_merchant or ?type=payment_acceptance): a prior
  // request of a DIFFERENT type must NOT short-circuit this one — an existing
  // merchant may add either on top of what she already has. Only a prior
  // request of the SAME add-on type gates its own form (so re-visiting after
  // an approval, say, still shows her that status rather than a blank form).
  const formIsAddOn = ADD_ON_BUSINESS_TYPES.includes(formType);
  const blockExisting = !formIsAddOn || existingType === formType;
  if (!blockExisting) return { screen: 'form' };

  if (existingType === 'liquidity_provider' && (existingRequest.status === 'pending' || existingRequest.status === 'approved')) {
    return { screen: 'liquidity' };
  }

  switch (existingRequest.status) {
    case 'pending':
      return { screen: 'pending', pm: existingIsPm };
    case 'approved':
      // An approved PM request only means "you're a Payment Merchant" once the
      // PM is actually active. Otherwise the activation is still pending/failed.
      if (existingIsPm && !pmActive) {
        // Only show "Activating…" when there is real provisioning data in flight.
        // A legacy approval with no settlement/maker data will never activate on
        // its own — send the user back to the form to supply the missing details
        // instead of dead-ending them on the provisioning screen.
        return hasPmProvisioningData(existingRequest)
          ? { screen: 'provisioning' }
          : { screen: 'form' };
      }
      return { screen: 'approved', pm: existingIsPm, payFlow: existingIsPay };
    case 'rejected':
      return { screen: 'rejected' };
    default:
      return { screen: 'form' };
  }
}

export default function MerchantRequestScreen() {
  const { colors, spacing } = useTheme();
  const { currentUser } = useAuth();
  // Pad the scroll body by the keyboard height so fields below the focused one
  // can scroll clear of the keypad (Android runs pan mode — see the hook).
  const kbHeight = useKeyboardHeight();
  const { type } = useLocalSearchParams<{ type?: string }>();
  const [loading, setLoading] = useState(true);
  const [submitting, setSubmitting] = useState(false);
  const [existingRequest, setExistingRequest] = useState<any>(null);
  // Actual Payment Merchant status (GET /p2p/pm/me) — distinct from an approved
  // PM *request*. Used to gate the "You're a Payment Merchant!" success screen.
  const [pmActive, setPmActive] = useState(false);
  // The applicant's KYC status: AuthContext's copy, replaced by a fresh read
  // each time the screen gains focus (she may have just verified). If the read
  // fails, the cached copy stands. kycChecked holds the TSA Pay screen on its
  // spinner until the first read settles, so neither the form nor the verify
  // card flashes up first. kycRefused is the server refusing the submit for
  // KYC, which outranks any stale copy.
  const [kycStatus, setKycStatus] = useState<string | undefined>(currentUser?.verificationStatus);
  const [kycChecked, setKycChecked] = useState(false);
  const [kycRefused, setKycRefused] = useState(false);

  const [businessType, setBusinessType] = useState(() => initBusinessType(type));
  const [businessName, setBusinessName] = useState('');
  const [businessDescription, setBusinessDescription] = useState('');
  const [address, setAddress] = useState('');
  const [city, setCity] = useState('');
  const [state, setState] = useState('');
  const [country, setCountry] = useState('');
  const [phone, setPhone] = useState(currentUser?.phoneNumber || '');
  const [registrationNumber, setRegistrationNumber] = useState('');
  const [website, setWebsite] = useState('');

  // ─── PM fields ──────────────────────────────────────────────────────────────
  const [banks, setBanks] = useState<NgBank[]>([]);
  const [banksLoading, setBanksLoading] = useState(false);
  const [settlementBank, setSettlementBank] = useState<NgBank | null>(null);
  const [showBankPicker, setShowBankPicker] = useState(false);
  const [settlementAccountNumber, setSettlementAccountNumber] = useState('');
  const [settlementAccountName, setSettlementAccountName] = useState('');
  const [makerWalletAddress, setMakerWalletAddress] = useState('');
  const [perTradeCap, setPerTradeCap] = useState('');
  const [dailyCap, setDailyCap] = useState('');

  // Name-enquiry state for settlement account
  const [settleResolving, setSettleResolving] = useState(false);
  const [settleResolved, setSettleResolved] = useState(false);
  const [settleResolveError, setSettleResolveError] = useState<null | 'not_found' | 'unavailable'>(null);

  // Inline validation errors for PM fields (shown after first submit attempt)
  const [pmErrors, setPmErrors] = useState<PmValidationErrors>({});
  const [pmValidated, setPmValidated] = useState(false);

  const scrollRef = useRef<ScrollView>(null);
  const businessNameRef = useRef<TextInput>(null);
  const descriptionRef = useRef<TextInput>(null);
  const addressRef = useRef<TextInput>(null);
  const phoneRef = useRef<TextInput>(null);
  const regNumberRef = useRef<TextInput>(null);
  const websiteRef = useRef<TextInput>(null);
  const makerWalletRef = useRef<TextInput>(null);
  const perTradeCapRef = useRef<TextInput>(null);
  const dailyCapRef = useRef<TextInput>(null);

  const isPm = businessType === 'p2p_merchant';
  const isPay = businessType === 'payment_acceptance';
  const screenTitle = 'Merchant Application';
  const needsBanks = isPm;

  useEffect(() => {
    fetchExistingRequest();
  }, []);

  useFocusEffect(
    useCallback(() => {
      if (!isPay) return;
      let cancelled = false;
      api.getKYCStatus()
        .then((res) => {
          if (cancelled || !res.success || !res.data) return;
          setKycStatus(res.data.verificationStatus);
          if (res.data.verificationStatus === 'verified') setKycRefused(false);
        })
        .catch(() => { /* keep the cached status; the server still gates the submit */ })
        .finally(() => { if (!cancelled) setKycChecked(true); });
      return () => { cancelled = true; };
    }, [isPay]),
  );

  // Load bank list when PM type is selected (lazy)
  useEffect(() => {
    if (!needsBanks || banks.length > 0) return;
    let cancelled = false;
    setBanksLoading(true);
    getNigerianBanks().then((list) => {
      if (!cancelled) {
        setBanks(list);
        setBanksLoading(false);
      }
    });
    return () => { cancelled = true; };
    // banks.length is intentionally omitted: we only want to load once when the
    // PM section is first shown. The banks array is stable after initial fetch.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [needsBanks]);

  // Debounced NUBAN name resolution for settlement account
  useEffect(() => {
    if (!needsBanks || !settlementBank || settlementAccountNumber.length !== 10) {
      setSettleResolving(false);
      setSettleResolved(false);
      setSettleResolveError(null);
      if (settlementAccountNumber.length < 10) {
        setSettlementAccountName('');
      }
      return;
    }
    let cancelled = false;
    setSettleResolving(true);
    setSettleResolved(false);
    setSettleResolveError(null);
    const timer = setTimeout(async () => {
      const res = await resolveAccountName(settlementBank.code, settlementAccountNumber);
      if (cancelled) return;
      setSettleResolving(false);
      if (res.ok) {
        setSettlementAccountName(res.accountName);
        setSettleResolved(true);
      } else {
        setSettlementAccountName('');
        setSettleResolveError(res.reason);
      }
    }, 400);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [needsBanks, settlementBank, settlementAccountNumber]);

  // Live-revalidate PM fields after first submit attempt
  useEffect(() => {
    if (!pmValidated) return;
    setPmErrors(validatePmFields({
      settlementBankCode: settlementBank?.code ?? '',
      settlementAccountNumber,
      settlementAccountName,
      makerWalletAddress,
    }));
  }, [pmValidated, settlementBank, settlementAccountNumber, settlementAccountName, makerWalletAddress]);

  const bankItems: PickerItem[] = useMemo(
    () => banks.map((b) => ({ id: b.code, label: b.name, icon: 'bank' as const })),
    [banks],
  );

  // Populate the shared business fields from an existing merchant request,
  // tolerating both camelCase (API default) and snake_case. Only fills empty
  // values so it never clobbers anything the user has already typed.
  const prefillFromExistingRequest = (r: any) => {
    const pick = (camel: string, snake: string) => (r?.[camel] ?? r?.[snake] ?? '') as string;
    setBusinessName((prev: string) => prev || pick('businessName', 'business_name'));
    setBusinessDescription((prev: string) => prev || pick('businessDescription', 'business_description'));
    setAddress((prev: string) => prev || pick('address', 'address'));
    setCity((prev: string) => prev || pick('city', 'city'));
    setState((prev: string) => prev || pick('state', 'state'));
    setCountry((prev: string) => prev || pick('country', 'country'));
    setPhone((prev: string) => prev || pick('phone', 'phone'));
    setRegistrationNumber((prev: string) => prev || pick('registrationNumber', 'registration_number'));
  };

  const fetchExistingRequest = async () => {
    setLoading(true);
    try {
      // Fetch the existing request and the real PM status together. getMyPmStatus
      // is dormant-safe (returns {isPm:false} on any error), so it never blocks.
      // An add-on form gates on its own type's request: the newest request
      // when it is that type (or there is none), else one typed fetch. The
      // role cache and prefill below keep reading the untyped newest request.
      const filter = requestTypeFilter(businessType);
      const [res, pmStatus] = await Promise.all([getMyMerchantRequest(), getMyPmStatus()]);
      setPmActive(pmStatus.isPm);
      if (filter && res.success) {
        const newestType = res.data?.businessType ?? res.data?.business_type;
        const typed = !res.data || newestType === filter ? res : await getMyMerchantRequest(filter);
        if (!typed.success) {
          Alert.alert('Error', typed.message || 'Could not check your application status. Please try again.');
          return;
        }
        setExistingRequest(typed.data ?? null);
      }
      if (res.success && res.data) {
        if (!filter) setExistingRequest(res.data);
        // TSA Pay (payment_acceptance) approvals never grant the marketplace
        // RoleMerchant server-side (Task 4's deliberate design) — caching
        // role='merchant' here would show the "Go to Merchant Dashboard" gate
        // for a capability the user doesn't have, whose API calls would 403.
        const approvedType = res.data.businessType ?? res.data.business_type;
        if (res.data.status === 'approved' && cachesMerchantRole(approvedType)) {
          await AsyncStorage.setItem('role', 'merchant');
        }
        // Pre-fill the shared business fields from the existing request. This
        // matters on the PM-upgrade path (?type=p2p_merchant): an existing
        // general merchant falls through to the form, and shouldn't have to
        // re-type details they already submitted. Values are ignored on the
        // gate status screens, so populating unconditionally is harmless.
        prefillFromExistingRequest(res.data);
      } else if (!res.success) {
        Alert.alert('Error', res.message || 'Could not check your application status. Please try again.');
      }
    } catch {
      Alert.alert('Error', 'Network error. Please check your connection and try again.');
    } finally {
      setLoading(false);
    }
  };

  const handleSubmit = async () => {
    if (!businessName || !address || !city || !state || !country || !phone) {
      Alert.alert('Error', 'Please fill in all required fields');
      return;
    }

    // A website is optional — a business without one takes payments through
    // payment links — but one that is given must be an https address.
    if (isPay && website.trim() && !website.trim().toLowerCase().startsWith('https://')) {
      Alert.alert('Error', "Enter your website's https address");
      return;
    }

    // Validate PM-specific fields before submitting
    if (isPm) {
      const errors = validatePmFields({
        settlementBankCode: settlementBank?.code ?? '',
        settlementAccountNumber,
        settlementAccountName,
        makerWalletAddress,
      });
      setPmErrors(errors);
      setPmValidated(true);
      if (Object.keys(errors).length > 0) {
        Alert.alert('Payment Merchant setup incomplete', 'Please fill in all required Payment Merchant fields correctly.');
        return;
      }
    }

    setSubmitting(true);
    const res = await submitMerchantRequest({
      businessType,
      businessName,
      businessDescription: businessDescription || undefined,
      address,
      city,
      state,
      country,
      phone,
      registrationNumber: registrationNumber || undefined,
      ...(isPay ? { website: website.trim() } : {}),
      ...(isPm ? {
        settlementBankCode: settlementBank!.code,
        settlementAccountNumber,
        settlementAccountName,
        makerWalletAddress: makerWalletAddress.trim(),
        requestedPerTradeCapUsd: perTradeCap ? parseFloat(perTradeCap) : 0,
        requestedDailyCapUsd: dailyCap ? parseFloat(dailyCap) : 0,
      } : {}),
    });

    setSubmitting(false);
    const refusal = res.success ? null : kycRefusalFor(res.code, res.message);
    if (refusal?.kind === 'verify') {
      setKycRefused(true);
      return;
    }
    if (refusal) {
      promptKycRefusal(res.code, res.message);
      return;
    }
    if (res.success) {
      Alert.alert('Success', 'Your merchant application has been submitted!');
      setExistingRequest(res.data);
    } else {
      Alert.alert('Error', res.message || 'Failed to submit application');
    }
  };

  if (loading || (isPay && !kycChecked)) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <Stack.Screen options={{ title: screenTitle }} />
        <View style={{ flex: 1, justifyContent: 'center', alignItems: 'center' }}>
          <Spinner size="large" label="Loading…" />
        </View>
      </SafeAreaView>
    );
  }

  // Decide which gate screen (if any) to show. resolveMerchantGate also factors
  // in the real PM-active status so an approved-but-not-yet-active PM request
  // shows a "provisioning" state rather than a false "You're a Payment Merchant!".
  const gate = resolveMerchantGate({
    existingRequest,
    formType: businessType,
    pmActive,
    kycVerified: kycStatus === 'verified' && !kycRefused,
  });

  if (gate.screen === 'verify') {
    return (
      <StatusScreen
        screenTitle={screenTitle}
        icon="shield"
        tone={colors.warning}
        toneBg={colors.warningBg}
        title="Verify your identity first"
        body="TSA Pay is for verified businesses. Verify your identity, then come back here to apply."
        actionTitle="Verify identity"
        actionVariant="primary"
        onAction={() => router.push(KYC_ROUTE as never)}
      />
    );
  }

  if (gate.screen === 'liquidity') {
    return (
      <StatusScreen
        screenTitle={screenTitle}
        icon="wallet"
        tone={colors.accent}
        toneBg={colors.accentMuted}
        title="Liquidity providers now activate themselves in the app"
        body="You no longer need an application to provide liquidity. Open TSA Liquidity to activate and add liquidity."
        actionTitle="Open TSA Liquidity"
        actionVariant="primary"
        onAction={() => router.push(LP_DASHBOARD_ROUTE as never)}
      />
    );
  }

  if (gate.screen === 'pending') {
    return (
      <StatusScreen
        screenTitle={screenTitle}
        icon="clock"
        tone={colors.warning}
        toneBg={colors.warningBg}
        title={gate.pm ? 'Payment Merchant Application Under Review' : 'Application Under Review'}
        body={`Your ${gate.pm ? 'Payment Merchant' : 'merchant'} application for "${existingRequest.businessName}" is being reviewed. We'll update your status soon.`}
        actionTitle="Go Back"
        actionVariant="secondary"
        onAction={() => router.back()}
      />
    );
  }

  if (gate.screen === 'provisioning') {
    return (
      <StatusScreen
        screenTitle={screenTitle}
        icon="clock"
        tone={colors.warning}
        toneBg={colors.warningBg}
        title="Activating your Payment Merchant account"
        body={`Your Payment Merchant application for "${existingRequest.businessName}" is approved, but your account is still being set up. This usually completes within a few minutes. If it doesn't, please contact support.`}
        actionTitle="Go Back"
        actionVariant="secondary"
        onAction={() => router.back()}
      />
    );
  }

  if (gate.screen === 'approved') {
    return (
      <StatusScreen
        screenTitle={screenTitle}
        icon="checkCircle"
        tone={colors.success}
        toneBg={colors.successBg}
        title={gate.payFlow ? 'Your TSA Pay application was approved' : gate.pm ? "You're a Payment Merchant!" : "You're a Merchant!"}
        body={
          gate.payFlow
            ? 'Your TSA Pay application has been approved. Choose where you want to be paid — a bank account in your business or verified name, or a crypto wallet.'
            : gate.pm
            ? 'Your Payment Merchant application is approved — you can publish PM ads and settle trades via bank transfer.'
            : 'Your application has been approved. You now have access to the merchant portal.'
        }
        actionTitle={gate.payFlow ? 'Payment Settings' : 'Go to Merchant Dashboard'}
        actionVariant="success"
        onAction={() => router.push(gate.payFlow ? '/merchants/pay-settings' : '/merchants/dashboard')}
      >
        {gate.payFlow && <SetUpOnWebCard />}
      </StatusScreen>
    );
  }

  if (gate.screen === 'rejected') {
    return (
      <StatusScreen
        screenTitle={screenTitle}
        icon="closeCircle"
        tone={colors.danger}
        toneBg={colors.dangerBg}
        title="Application Rejected"
        body={`Reason: ${existingRequest.adminNote || 'No reason provided'}`}
        actionTitle="Apply Again"
        actionVariant="primary"
        onAction={() => {
          setExistingRequest(null);
          setBusinessName('');
          setBusinessDescription('');
          setAddress('');
          setCity('');
          setState('');
          setCountry('');
          setRegistrationNumber('');
          setWebsite('');
          // Reset PM fields too
          setSettlementBank(null);
          setSettlementAccountNumber('');
          setSettlementAccountName('');
          setMakerWalletAddress('');
          setPerTradeCap('');
          setDailyCap('');
          setPmErrors({});
          setPmValidated(false);
        }}
      />
    );
  }

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <Stack.Screen options={{ title: screenTitle }} />
      <KeyboardAvoidingView
        style={{ flex: 1 }}
        behavior={Platform.OS === 'ios' ? 'padding' : undefined}
        keyboardVerticalOffset={Platform.OS === 'ios' ? 90 : 0}
      >
        <ScrollView
          ref={scrollRef}
          keyboardShouldPersistTaps="handled"
          contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] + kbHeight }}
        >
          <Text variant="bodySm" color="textSecondary" style={{ marginBottom: spacing.xl }}>
            Fill in your business details to apply
          </Text>

          <Text variant="bodySm" weight="600" color="textSecondary" style={{ marginBottom: spacing.sm }}>
            Business Type *
          </Text>
          {isPickerLocked(type) ? (
            // Locked single-chip: entered via ?type=p2p_merchant or
            // ?type=payment_acceptance — cannot be changed.
            <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: spacing.sm }}>
              <Chip
                label={BUSINESS_TYPES.find((bt) => bt.value === type)?.label ?? businessType}
                selected
              />
            </View>
          ) : (
            // Normal selectable chip row (no type param).
            <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: spacing.sm }}>
              {BUSINESS_TYPES.map((bt) => (
                <Chip
                  key={bt.value}
                  label={bt.label}
                  selected={businessType === bt.value}
                  onPress={() => setBusinessType(bt.value)}
                />
              ))}
            </View>
          )}

          <Field
            ref={businessNameRef}
            label="Business Name *"
            value={businessName}
            onChangeText={setBusinessName}
            placeholder="e.g. Business Name"
            returnKeyType="next"
            onSubmitEditing={() => descriptionRef.current?.focus()}
          />

          <Field
            ref={descriptionRef}
            label="Business Description"
            value={businessDescription}
            onChangeText={setBusinessDescription}
            placeholder="Describe your business..."
            multiline
            multilineHeight={80}
            returnKeyType="next"
            blurOnSubmit={true}
            onSubmitEditing={() => addressRef.current?.focus()}
          />

          <Field
            ref={addressRef}
            label="Address *"
            value={address}
            onChangeText={setAddress}
            placeholder="Street address"
            returnKeyType="done"
            onSubmitEditing={() => Keyboard.dismiss()}
          />

          {/* Country → State → City via the shared cascading picker (same one
              used in signup / profile / delivery), replacing free-text fields. */}
          <View style={{ marginBottom: spacing.md }}>
            <LocationPicker
              value={{ country, state, city }}
              onChange={(loc) => {
                setCountry(loc.country);
                setState(loc.state);
                setCity(loc.city);
              }}
              fields={['country', 'state', 'city']}
              required={['country', 'state', 'city']}
              labels={{ country: 'Country', state: 'State', city: 'City' }}
              showLabels
            />
          </View>

          <Field
            ref={phoneRef}
            label="Phone *"
            value={phone}
            onChangeText={setPhone}
            placeholder="+234..."
            keyboardType="phone-pad"
            returnKeyType="next"
            onSubmitEditing={() => regNumberRef.current?.focus()}
          />

          <Field
            ref={regNumberRef}
            label="Registration Number (optional)"
            value={registrationNumber}
            onChangeText={setRegistrationNumber}
            placeholder="e.g. RC-123456"
            returnKeyType={isPm || isPay ? 'next' : 'done'}
            onSubmitEditing={
              isPay
                ? () => websiteRef.current?.focus()
                : isPm
                ? () => makerWalletRef.current?.focus()
                : handleSubmit
            }
          />

          {/* ─── TSA Pay setup (payment_acceptance only) ─────────────────── */}
          {isPay && (
            <Field
              ref={websiteRef}
              label="Website (optional)"
              value={website}
              onChangeText={setWebsite}
              placeholder="https://example.com"
              autoCapitalize="none"
              autoCorrect={false}
              keyboardType="url"
              returnKeyType="done"
              onSubmitEditing={handleSubmit}
            />
          )}

          {/* ─── Payment Merchant setup (p2p_merchant only) ──────────────── */}
          {isPm && (
            <View
              style={{
                marginTop: spacing.xl,
                borderTopWidth: 1,
                borderTopColor: colors.border,
                paddingTop: spacing.xl,
              }}
            >
              <Text variant="bodyMed" weight="700" style={{ marginBottom: spacing.xs }}>
                Payment Merchant setup
              </Text>
              <Text variant="caption" color="textSecondary" style={{ marginBottom: spacing.lg }}>
                These details are required for your corporate P2P Merchant account.
              </Text>

              {/* Settlement bank picker */}
              <SelectRow
                label="Settlement Bank *"
                placeholder="Choose your settlement bank"
                value={settlementBank?.name}
                leftIcon="bank"
                onPress={() => setShowBankPicker(true)}
                error={pmErrors.settlementBank}
                style={{ marginBottom: spacing.md }}
              />

              {/* Settlement account number */}
              <View style={{ marginBottom: spacing.md }}>
                <Field
                  label="Settlement Account Number *"
                  value={settlementAccountNumber}
                  onChangeText={(v) => {
                    setSettlementAccountNumber(v.replace(/\D/g, ''));
                    if (settleResolved) {
                      setSettleResolved(false);
                      setSettlementAccountName('');
                    }
                  }}
                  placeholder="10-digit NUBAN"
                  keyboardType="number-pad"
                  maxLength={20}
                  returnKeyType="next"
                  error={pmErrors.settlementAccountNumber}
                />
              </View>

              {/* Settlement account name — auto-resolved */}
              <View style={{ marginBottom: spacing.md }}>
                <Text variant="bodySm" weight="600" color="textSecondary" style={{ marginBottom: spacing.xs }}>
                  Settlement Account Name *
                </Text>
                <View
                  style={{
                    flexDirection: 'row',
                    alignItems: 'center',
                    minHeight: 48,
                    paddingHorizontal: spacing.md,
                    borderRadius: 8,
                    borderWidth: pmErrors.settlementAccountName ? 1.5 : settleResolved ? 2 : 1.5,
                    borderColor: pmErrors.settlementAccountName
                      ? colors.danger
                      : settleResolved
                      ? colors.success
                      : colors.border,
                    backgroundColor: colors.surface,
                  }}
                >
                  <TextInput
                    style={{
                      flex: 1,
                      paddingVertical: spacing.md,
                      color: colors.textPrimary,
                    }}
                    value={settlementAccountName}
                    onChangeText={(v) => {
                      setSettlementAccountName(v);
                      if (settleResolved) setSettleResolved(false);
                    }}
                    placeholder="Auto-fills when you enter the account number"
                    placeholderTextColor={colors.textMuted}
                    editable={!settleResolving && !settleResolved}
                    autoCapitalize="words"
                  />
                  {settleResolving ? (
                    <Spinner size="small" />
                  ) : settleResolved ? (
                    <PressableScale
                      onPress={() => {
                        setSettleResolved(false);
                        setSettlementAccountName('');
                      }}
                      hitSlop={10}
                      accessibilityLabel="Clear and edit name"
                    >
                      <Icon name="checkCircle" size={20} color={colors.success} />
                    </PressableScale>
                  ) : null}
                </View>
                {settleResolveError === 'not_found' && (
                  <Text variant="caption" color="danger" style={{ marginTop: spacing.xs }}>
                    {"We couldn't find this account at the selected bank. Check the digits or bank."}
                  </Text>
                )}
                {settleResolveError === 'unavailable' && (
                  <Text variant="caption" color="textMuted" style={{ marginTop: spacing.xs }}>
                    Account verification is offline. You can type the name manually.
                  </Text>
                )}
                {pmErrors.settlementAccountName && !settleResolveError && (
                  <Text variant="caption" color="danger" style={{ marginTop: spacing.xs }}>
                    {pmErrors.settlementAccountName}
                  </Text>
                )}
              </View>

              {isPm && (
              <>
              {/* Maker wallet address */}
              <Field
                ref={makerWalletRef}
                label="Maker Wallet Address *"
                value={makerWalletAddress}
                onChangeText={setMakerWalletAddress}
                placeholder="0x…"
                autoCapitalize="none"
                autoCorrect={false}
                returnKeyType="next"
                onSubmitEditing={() => perTradeCapRef.current?.focus()}
                error={pmErrors.makerWalletAddress}
              />

              {/* Per-trade cap */}
              <Field
                ref={perTradeCapRef}
                label="Per-Trade Cap (USD)"
                value={perTradeCap}
                onChangeText={(v) => setPerTradeCap(v.replace(/[^0-9.]/g, ''))}
                placeholder="0 = unlimited"
                keyboardType="decimal-pad"
                returnKeyType="next"
                onSubmitEditing={() => dailyCapRef.current?.focus()}
              />

              {/* Daily cap */}
              <Field
                ref={dailyCapRef}
                label="Daily Cap (USD)"
                value={dailyCap}
                onChangeText={(v) => setDailyCap(v.replace(/[^0-9.]/g, ''))}
                placeholder="0 = unlimited"
                keyboardType="decimal-pad"
                returnKeyType="done"
                onSubmitEditing={handleSubmit}
              />
              </>
              )}
            </View>
          )}

          <Button
            title="Submit Application"
            onPress={handleSubmit}
            disabled={submitting}
            loading={submitting}
            style={{ marginTop: spacing.xl }}
          />
        </ScrollView>
      </KeyboardAvoidingView>

      {/* Bank picker sheet (PM path only) */}
      <PickerSheet
        visible={showBankPicker}
        onClose={() => setShowBankPicker(false)}
        title="Choose settlement bank"
        items={bankItems}
        loading={banksLoading && banks.length === 0}
        selectedId={settlementBank?.code}
        emptyText="No banks found."
        onSelect={(item) => {
          const b = banks.find((x) => x.code === item.id);
          if (!b) return;
          setSettlementBank(b);
          setShowBankPicker(false);
          // Clearing account data when bank changes so NUBAN re-resolves
          setSettlementAccountNumber('');
          setSettlementAccountName('');
          setSettleResolved(false);
          setSettleResolveError(null);
        }}
      />
    </SafeAreaView>
  );
}

// Token-themed labelled text field. Mirrors the @/components/ui Input chrome
// (label, accent focus border, surface fill, token radius/spacing) while
// forwarding its ref to the underlying TextInput so the onSubmitEditing focus
// chain keeps working exactly as before.
interface FieldProps extends Omit<TextInputProps, 'style'> {
  label: string;
  multilineHeight?: number;
  error?: string;
}

const Field = React.forwardRef<TextInput, FieldProps>(
  ({ label, multilineHeight, error, onFocus, onBlur, ...rest }, ref) => {
    const { colors, radius, spacing, fonts, type } = useTheme();
    const [focused, setFocused] = useState(false);
    return (
      <View style={{ marginTop: spacing.md }}>
        <Text variant="bodySm" weight="600" color="textSecondary" style={{ marginBottom: spacing.xs }}>
          {label}
        </Text>
        <TextInput
          ref={ref}
          placeholderTextColor={colors.textMuted}
          onFocus={(e) => {
            setFocused(true);
            onFocus?.(e);
          }}
          onBlur={(e) => {
            setFocused(false);
            onBlur?.(e);
          }}
          style={{
            paddingHorizontal: spacing.md,
            paddingVertical: spacing.md,
            borderRadius: radius.md,
            borderWidth: focused ? 2 : 1.5,
            borderColor: error ? colors.danger : focused ? colors.accent : colors.border,
            backgroundColor: colors.surface,
            color: colors.textPrimary,
            fontFamily: fonts['400'],
            fontSize: type.body.fontSize,
            ...(multilineHeight ? { height: multilineHeight, textAlignVertical: 'top' } : null),
          }}
          {...rest}
        />
        {error && (
          <Text variant="caption" color="danger" style={{ marginTop: spacing.xs }}>
            {error}
          </Text>
        )}
      </View>
    );
  }
);
Field.displayName = 'Field';

// An approved TSA Pay merchant sets up and tests on the web app; the app keeps
// only where she is paid (Payment Settings, the screen's own action).
export const SetUpOnWebCard: React.FC = () => {
  const { colors, spacing, radius } = useTheme();
  return (
    <View
      style={{
        alignSelf: 'stretch',
        borderRadius: radius.md,
        borderWidth: 1,
        borderColor: colors.border,
        padding: spacing.lg,
        marginBottom: spacing.xl,
      }}
    >
      <Text variant="bodyMed" weight="700" style={{ marginBottom: spacing.xs }}>
        Set up on the web
      </Text>
      <Text variant="bodySm" color="textSecondary" style={{ marginBottom: spacing.md }}>
        {`Create API keys, webhooks and payouts, and see your payments, at ${COMPANY.webApp.replace(/^https:\/\//, '')}`}
      </Text>
      <Button title="Open" variant="secondary" onPress={() => Linking.openURL(COMPANY.webApp)} />
    </View>
  );
};

// Shared status panel for the verify / pending / approved / rejected branches —
// keeps the status registration + actions identical, only the chrome is themed.
// children render between the body and the action.
const StatusScreen: React.FC<{
  icon: 'clock' | 'checkCircle' | 'closeCircle' | 'shield' | 'wallet';
  tone: string;
  toneBg: string;
  title: string;
  body: string;
  actionTitle: string;
  actionVariant: 'primary' | 'secondary' | 'success';
  onAction: () => void;
  children?: React.ReactNode;
  screenTitle: string;
}> = ({ screenTitle, icon, tone, toneBg, title, body, actionTitle, actionVariant, onAction, children }) => {
  const { colors, spacing, radius } = useTheme();
  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <Stack.Screen options={{ title: screenTitle }} />
      <View style={{ flex: 1, justifyContent: 'center', paddingHorizontal: spacing.lg }}>
        <View
          style={{
            backgroundColor: colors.surface,
            borderRadius: radius.lg,
            borderWidth: 1,
            borderColor: tone,
            padding: spacing['2xl'],
            alignItems: 'center',
          }}
        >
          <View
            style={{
              width: 72,
              height: 72,
              borderRadius: radius.pill,
              backgroundColor: toneBg,
              justifyContent: 'center',
              alignItems: 'center',
              marginBottom: spacing.lg,
            }}
          >
            <Icon name={icon} size={36} color={tone} />
          </View>
          <Text variant="title2" weight="700" align="center" style={{ marginBottom: spacing.sm }}>
            {title}
          </Text>
          <Text variant="bodySm" color="textSecondary" align="center" style={{ marginBottom: spacing.xl }}>
            {body}
          </Text>
          {children}
          <Button title={actionTitle} variant={actionVariant} onPress={onAction} />
        </View>
      </View>
    </SafeAreaView>
  );
};
