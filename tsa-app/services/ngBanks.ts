// services/ngBanks.ts
//
// Nigerian bank directory + NUBAN name enquiry used by the P2P
// payment-method form.
//
// Bank list — three-tier source with graceful degradation:
//   1. Our backend's Nomba-backed `GET /p2p/banks`        (preferred)
//   2. Public `https://nigerianbanks.xyz/`                (no-auth fallback)
//   3. Curated 17-bank hardcoded list                     (offline last resort)
// All three use CBN/NIBSS codes, so a bank picked from tier 2/3 still
// works when the form submits and (later) when account resolution runs
// against tier 1 again.
//
// Name enquiry — only the backend can do this (NUBAN resolution requires
// a server-held secret no matter the provider), so there is no client-side
// fallback. On outage the UI lets the user type the name manually.
//
// Caching: the bank list is stable, so we keep an AsyncStorage cache
// (7 days) and serve it instantly. The backend also caches 24h, so the
// network call is cheap when we do miss.

import AsyncStorage from '@react-native-async-storage/async-storage';
import { banksApi, type NgBank } from './p2p';

export type { NgBank } from './p2p';

const CACHE_KEY = 'ng_banks_v2';
const CACHE_TTL_MS = 7 * 24 * 60 * 60 * 1000;

const FALLBACK: NgBank[] = [
  { name: 'Access Bank', code: '044' },
  { name: 'Ecobank Nigeria', code: '050' },
  { name: 'FCMB', code: '214' },
  { name: 'Fidelity Bank', code: '070' },
  { name: 'First Bank of Nigeria', code: '011' },
  { name: 'Guaranty Trust Bank', code: '058' },
  { name: 'Kuda Bank', code: '50211' },
  { name: 'Moniepoint MFB', code: '50515' },
  { name: 'OPay', code: '999992' },
  { name: 'PalmPay', code: '999991' },
  { name: 'Polaris Bank', code: '076' },
  { name: 'Stanbic IBTC Bank', code: '221' },
  { name: 'Sterling Bank', code: '232' },
  { name: 'Union Bank of Nigeria', code: '032' },
  { name: 'United Bank for Africa', code: '033' },
  { name: 'Wema Bank', code: '035' },
  { name: 'Zenith Bank', code: '057' },
];

interface CacheEntry {
  ts: number;
  banks: NgBank[];
}

export async function getNigerianBanks(): Promise<NgBank[]> {
  const cached = await readCache();
  if (cached) {
    if (Date.now() - cached.ts > CACHE_TTL_MS) {
      refreshAndCache().catch(() => {});
    }
    return cached.banks;
  }
  return await refreshAndCache();
}

async function readCache(): Promise<CacheEntry | null> {
  try {
    const raw = await AsyncStorage.getItem(CACHE_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw);
    if (
      parsed &&
      typeof parsed.ts === 'number' &&
      Array.isArray(parsed.banks) &&
      parsed.banks.length > 0
    ) {
      return parsed as CacheEntry;
    }
  } catch {}
  return null;
}

async function refreshAndCache(): Promise<NgBank[]> {
  const banks = (await fetchFromBackend()) ?? (await fetchFromPublicSource());
  if (!banks || banks.length === 0) return FALLBACK;

  const sorted = [...banks].sort((a, b) => a.name.localeCompare(b.name));
  try {
    await AsyncStorage.setItem(
      CACHE_KEY,
      JSON.stringify({ ts: Date.now(), banks: sorted } as CacheEntry)
    );
  } catch {}
  return sorted;
}

async function fetchFromBackend(): Promise<NgBank[] | null> {
  const res = await banksApi.list();
  if (res.success && res.data && res.data.length > 0) return res.data;
  return null;
}

// Public, no-auth fallback. Schema differs (logo/slug/ussd extras) so we
// project down to just code+name.
const PUBLIC_SOURCE_URL = 'https://nigerianbanks.xyz/';
async function fetchFromPublicSource(): Promise<NgBank[] | null> {
  try {
    const res = await fetch(PUBLIC_SOURCE_URL);
    if (!res.ok) return null;
    const data = await res.json();
    if (!Array.isArray(data)) return null;
    const banks: NgBank[] = data
      .filter(
        (b: any) =>
          b && typeof b.name === 'string' && typeof b.code === 'string'
      )
      .map((b: any) => ({ name: b.name, code: b.code }));
    return banks.length > 0 ? banks : null;
  } catch {
    return null;
  }
}

/**
 * Result of an account name enquiry.
 *
 * - `ok: true`  — Nomba resolved a holder name.
 * - `ok: false` + `reason: 'not_found'` — account does not exist at that bank
 *   (or bankCode/number combo is wrong). UI should let the user retry.
 * - `ok: false` + `reason: 'unavailable'` — backend/Nomba outage. UI should
 *   fall back to letting the user type the name themselves.
 */
export type ResolveAccountResult =
  | { ok: true; accountName: string }
  | { ok: false; reason: 'not_found' | 'unavailable'; message?: string };

export async function resolveAccountName(
  bankCode: string,
  accountNumber: string
): Promise<ResolveAccountResult> {
  const res = await banksApi.resolveAccount(bankCode, accountNumber);
  if (res.success && res.data?.accountName) {
    return { ok: true, accountName: res.data.accountName };
  }
  if (res.status === 404) {
    return { ok: false, reason: 'not_found', message: res.message };
  }
  return { ok: false, reason: 'unavailable', message: res.message };
}
