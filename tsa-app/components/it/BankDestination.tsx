// components/it/BankDestination.tsx
//
// Bank-transfer payout destination: bank picker, account number, and the
// NUBAN name-enquiry flow (debounced resolve + its three UI states). Extracted
// out of the MCGP sell screen (app/wallet/it/sell.tsx) so the stablecoin sell
// screen can reuse the identical bank-verification behaviour.
//
// Fully controlled — `value`/`onChange` are the single source of truth for the
// bank/account fields; this component's own state is only the ephemeral,
// non-reported bits (bank list, picker visibility, resolve status).
import React, { useEffect, useState } from 'react';
import {
  Icon,
  Input,
  PickerSheet,
  PressableScale,
  SelectRow,
  Spinner,
} from '@/components/ui';
import type { PickerItem } from '@/components/ui';
import { useTheme } from '@/theme';
import { getBanksForCountry, resolveAccountName, type NgBank } from '@/services/ngBanks';

export interface BankDestinationValue {
  bankCode: string;
  bankName: string;
  accountNumber: string;
  accountName: string;
  ready: boolean;
}

export interface BankDestinationProps {
  currency: string;
  value: BankDestinationValue;
  /**
   * MUST be referentially stable (a `useState` setter, or a `useCallback`
   * with an empty/stable dep list). It is a dependency of this component's
   * bank-load and debounced name-enquiry effects — an inline arrow re-created
   * on every parent render would re-run both on every keystroke elsewhere on
   * the screen, restarting the 400ms name-enquiry debounce and re-fetching
   * the bank list.
   */
  onChange: (next: BankDestinationValue) => void;
}

// Country + expected account-number length by payout currency. Kept local
// (rather than imported from sell.tsx, a route file) so this component has no
// dependency on either sell screen. Digit counts: NUBAN is exactly 10;
// Ghanaian and IBAN-style numbers are longer and variable, so this is an
// upper bound plus a "long enough to be real" floor, not an exact match — a
// valid foreign account is never rejected for not looking Nigerian.
const COUNTRY_BY_CURRENCY: Record<string, string> = {
  NGN: 'NG',
  GHS: 'GH',
  USD: 'US',
  EUR: '',
  GBP: 'GB',
};
const ACCOUNT_DIGITS_BY_CURRENCY: Record<string, number> = {
  NGN: 10,
  GHS: 13,
  USD: 12,
  EUR: 34,
  GBP: 8,
};
function countryForCurrency(currency: string): string {
  return COUNTRY_BY_CURRENCY[currency] ?? '';
}
function acctDigitsForCurrency(currency: string): number {
  return ACCOUNT_DIGITS_BY_CURRENCY[currency] ?? 34;
}

function calcReady(
  bankCode: string,
  accountNumber: string,
  accountName: string,
  resolving: boolean,
  resolveError: null | 'not_found' | 'unavailable',
  currency: string
): boolean {
  const acctDigits = acctDigitsForCurrency(currency);
  return (
    !!bankCode &&
    accountNumber.trim().length >= Math.min(8, acctDigits) &&
    accountNumber.trim().length <= acctDigits &&
    accountName.trim().length >= 2 &&
    !resolving &&
    resolveError !== 'not_found'
  );
}

export function BankDestination({ currency, value, onChange }: BankDestinationProps) {
  const { colors } = useTheme();
  const [banks, setBanks] = useState<NgBank[]>([]);
  const [banksLoading, setBanksLoading] = useState(true);
  const [showBankPicker, setShowBankPicker] = useState(false);
  // NUBAN name enquiry — validates the account number against the selected
  // bank (GET /p2p/banks/resolve → Flutterwave account-resolve when FW is live).
  const [resolving, setResolving] = useState(false);
  const [resolved, setResolved] = useState(false);
  const [resolveError, setResolveError] = useState<null | 'not_found' | 'unavailable'>(null);

  // Bank list follows the selected currency's country. Re-runs on change and
  // clears the previous selection — a Nigerian bank code is meaningless on a
  // Ghanaian rail, and silently keeping it would fail at payout.
  useEffect(() => {
    let cancelled = false;
    setBanksLoading(true);
    onChange({ bankCode: '', bankName: '', accountNumber: '', accountName: '', ready: false });
    (async () => {
      const list = await getBanksForCountry(countryForCurrency(currency));
      if (!cancelled) {
        setBanks(list);
        setBanksLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [currency, onChange]);

  // Debounced account-name lookup. Fires when a bank is selected and the
  // account number is a full 10-digit NUBAN — validates the number and
  // auto-fills the holder name. On provider outage the user can type the name.
  useEffect(() => {
    const { bankCode, bankName, accountNumber, accountName } = value;
    // Name enquiry is NUBAN-specific — the provider resolves Nigerian account
    // numbers only. For other rails the user types the holder name, which is
    // why readiness does not require `resolved`.
    if (currency !== 'NGN' || !bankCode || accountNumber.length !== 10) {
      setResolving(false);
      setResolved(false);
      setResolveError(null);
      onChange({
        bankCode,
        bankName,
        accountNumber,
        accountName,
        ready: calcReady(bankCode, accountNumber, accountName, false, null, currency),
      });
      return;
    }
    let cancelled = false;
    setResolving(true);
    setResolved(false);
    setResolveError(null);
    onChange({
      bankCode,
      bankName,
      accountNumber,
      accountName,
      ready: calcReady(bankCode, accountNumber, accountName, true, null, currency),
    });
    const timer = setTimeout(async () => {
      const res = await resolveAccountName(bankCode, accountNumber);
      if (cancelled) return;
      setResolving(false);
      if (res.ok) {
        setResolved(true);
        onChange({
          bankCode,
          bankName,
          accountNumber,
          accountName: res.accountName,
          ready: calcReady(bankCode, accountNumber, res.accountName, false, null, currency),
        });
      } else {
        setResolveError(res.reason);
        onChange({
          bankCode,
          bankName,
          accountNumber,
          accountName: '',
          ready: calcReady(bankCode, accountNumber, '', false, res.reason, currency),
        });
      }
    }, 400);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [currency, value.bankCode, value.accountNumber, onChange]);

  const bankItems: PickerItem[] = banks.map((b) => ({
    id: b.code,
    label: b.name,
    icon: 'bank' as const,
  }));

  return (
    <>
      <SelectRow
        label="Bank"
        placeholder={banksLoading ? 'Loading banks…' : 'Select bank'}
        value={value.bankName || undefined}
        leftIcon="bank"
        disabled={banksLoading && banks.length === 0}
        onPress={() => setShowBankPicker(true)}
      />
      <Input
        label="Account number"
        placeholder="10-digit NUBAN"
        keyboardType="number-pad"
        maxLength={10}
        value={value.accountNumber}
        onChangeText={(v) => {
          const accountNumber = v.replace(/\D/g, '').slice(0, 10);
          onChange({
            ...value,
            accountNumber,
            ready: calcReady(value.bankCode, accountNumber, value.accountName, resolving, resolveError, currency),
          });
        }}
      />
      <Input
        label="Account holder name"
        placeholder={
          value.bankCode && value.accountNumber.length === 10
            ? 'Looking up account…'
            : 'Auto-fills when account number is valid'
        }
        autoCapitalize="words"
        value={value.accountName}
        onChangeText={(v) => {
          if (resolved) setResolved(false);
          onChange({
            ...value,
            accountName: v,
            ready: calcReady(value.bankCode, value.accountNumber, v, resolving, resolveError, currency),
          });
        }}
        editable={!resolving && !resolved}
        error={
          resolveError === 'not_found'
            ? 'We couldn’t find this account at the selected bank. Check the digits or bank.'
            : undefined
        }
        helper={
          resolveError === 'unavailable'
            ? 'Account verification is offline. You can type the name manually.'
            : resolved
              ? 'Account verified'
              : undefined
        }
        rightAdornment={
          resolving ? (
            <Spinner size="small" />
          ) : resolved ? (
            <PressableScale
              onPress={() => {
                setResolved(false);
                onChange({
                  ...value,
                  accountName: '',
                  ready: calcReady(value.bankCode, value.accountNumber, '', resolving, resolveError, currency),
                });
              }}
              hitSlop={10}
              accessibilityLabel="Clear and edit name"
            >
              <Icon name="checkCircle" size={20} color={colors.success} />
            </PressableScale>
          ) : undefined
        }
      />

      <PickerSheet
        visible={showBankPicker}
        onClose={() => setShowBankPicker(false)}
        title="Select bank"
        items={bankItems}
        loading={banksLoading && banks.length === 0}
        selectedId={value.bankCode || undefined}
        emptyText="No banks available right now."
        onSelect={(item) => {
          const b = banks.find((x) => x.code === item.id) ?? null;
          setShowBankPicker(false);
          setResolved(false);
          setResolveError(null);
          const bankCode = b?.code ?? '';
          const bankName = b?.name ?? '';
          onChange({
            bankCode,
            bankName,
            accountNumber: value.accountNumber,
            accountName: '',
            ready: calcReady(bankCode, value.accountNumber, '', resolving, null, currency),
          });
        }}
      />
    </>
  );
}
