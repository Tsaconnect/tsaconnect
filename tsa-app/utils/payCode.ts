// utils/payCode.ts
//
// A TSA Pay payment code as a payer types it: "TSA-7K2QF-M9XWD". Mirrors the
// API's NormalizePayCode (tsa-api-go/internal/services/pay_code.go) — change
// both together; the server re-normalises whatever is sent. Depends only on
// zero-dependency modules, so the screen and its tests import it freely.
import { COMPANY } from '@/constants/legal/company';
import { isPaymentId } from '@/utils/paymentId';
import { strictHttpsHost } from '@/utils/strictHttpsHost';

const ALPHABET = '0123456789ABCDEFGHJKMNPQRSTVWXYZ';
const CODE_LENGTH = 10;
const PREFIX = 'TSA';
const MAX_INPUT_BYTES = 64;

export const PAY_CODE_NOT_FOUND = "We couldn't find that code. Check it and try again.";
export const PAY_CODE_TOO_MANY = 'Too many tries — wait a minute.';

// The API's payCodeIgnored list, as the app's two sets — the same explicit
// code points, never a Unicode category whose contents move with the runtime.
// Whitespace is Go's unicode.IsSpace set (JS \s differs: it also matches
// U+FEFF, which the API refuses as non-ASCII).
const GO_SPACES = new Set([0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x20, 0x85, 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000]
  .concat(Array.from({ length: 11 }, (_, i) => 0x2000 + i)));
// Dash punctuation as of Unicode 15 (Pd), listed out: Hermes and the Jest transform lack \p{…} escapes.
const DASHES = new Set(
  [0x2d, 0x58a, 0x5be, 0x1400, 0x1806, 0x2e17, 0x2e1a, 0x2e3a, 0x2e3b, 0x2e40, 0x2e5d, 0x301c, 0x3030, 0x30a0,
    0xfe31, 0xfe32, 0xfe58, 0xfe63, 0xff0d, 0x10ead]
    .concat(Array.from({ length: 6 }, (_, i) => 0x2010 + i)),
);

function utf8Length(s: string): number {
  let n = 0;
  for (const ch of s) {
    const cp = ch.codePointAt(0)!;
    n += cp < 0x80 ? 1 : cp < 0x800 ? 2 : cp < 0x10000 ? 3 : 4;
  }
  return n;
}

/** Upper-cased, a whole pasted TSA-… code's prefix dropped, O read as 0 and I/L as 1. */
function forgive(raw: string): string {
  let code = raw.toUpperCase();
  if (code.length === PREFIX.length + CODE_LENGTH && code.startsWith(PREFIX)) code = code.slice(PREFIX.length);
  return code.replace(/O/g, '0').replace(/[IL]/g, '1');
}

/** The bare ten-character code, or null when what she typed can't be one. */
export function normalizePayCode(input: string): string | null {
  if (utf8Length(input) > MAX_INPUT_BYTES) return null;
  let kept = '';
  for (const ch of input) {
    if (GO_SPACES.has(ch.codePointAt(0)!) || DASHES.has(ch.codePointAt(0)!)) continue;
    // Checked before upper-casing, which folds ı to I and ſ to S: a code is ASCII.
    if (ch.codePointAt(0)! > 0x7f) return null;
    kept += ch;
  }
  const code = forgive(kept);
  if (code.length !== CODE_LENGTH) return null;
  for (const ch of code) if (!ALPHABET.includes(ch)) return null;
  return code;
}

/**
 * A "TSA" typed or pasted ahead of a separator is the prefix, not the start of a code
 * (a bare code starting TSA has a code character right after it): drop it, so
 * typing T-S-A-dash key by key lands on an empty field, ready for the code.
 */
export function dropTypedPrefix(input: string): string {
  const m = /^\s*TSA(?=[\s\-\u2010-\u2015])/i.exec(input);
  return m ? input.slice(m[0].length) : input;
}

/**
 * What the field shows while she types or pastes: at most ten code characters
 * as "XXXXX-XXXXX" (the "TSA-" is drawn beside the field, never in it, so it
 * can't be typed twice). A leading TSA is dropped once what she typed runs past
 * ten characters — typed with no dash, TSA + the code fills the field before
 * the code is done, and the TSA was the prefix.
 */
export function payCodeFieldText(input: string): string {
  let typed = input.replace(/[^0-9A-Za-z]/g, '').toUpperCase();
  if (typed.length > CODE_LENGTH && typed.startsWith(PREFIX)) typed = typed.slice(PREFIX.length);
  const code = forgive(typed)
    .split('')
    .filter((ch) => ALPHABET.includes(ch))
    .join('')
    .slice(0, CODE_LENGTH);
  return code.length > 5 ? `${code.slice(0, 5)}-${code.slice(5)}` : code;
}

/** The line shown when a lookup fails: not found and rate-limited in her words, else the server's. */
export function payCodeLookupError(err: unknown): string {
  const e = (err ?? {}) as { status?: number; message?: string };
  if (e.status === 404) return PAY_CODE_NOT_FOUND;
  if (e.status === 429) return PAY_CODE_TOO_MANY;
  return e.message || 'Could not look up that code. Try again.';
}

/** What the Pay screen can open from a pasted or typed value. */
export type PayInput = { kind: 'code'; code: string } | { kind: 'payment'; id: string };

/** Our site's hosts, the apex and www.: where a checkout link or a receipt can be (the server's checkout base may be either). */
export const TSA_SITE_HOSTS: readonly string[] = [COMPANY.domain, `www.${COMPANY.domain}`];

/**
 * A payment code, or a one-time pay link — https://tsaconnectworld.com/pay/<id>
 * or the app's own tsaconnect://pay?id=<id> — read down to the payment id it
 * names. Anything else, including a reusable /pay/l/<slug> link (which opens in
 * the browser, where the customer enters the amount), is null. Only the id is
 * ever kept, and only once isPaymentId accepts it: the rest of the URL is
 * whoever sent it.
 */
export function parsePayInput(input: string): PayInput | null {
  const text = input.trim();
  const code = normalizePayCode(text);
  if (code) return { kind: 'code', code };
  const app = /^tsaconnect:\/\/pay\?(?:[^#]*&)?id=([^&#]+)/i.exec(text);
  if (app) return isPaymentId(app[1]) ? { kind: 'payment', id: app[1].toLowerCase() } : null;
  // strictHttpsHost, never URL alone: React Native's URL reads
  // "https://evil.example#@tsaconnectworld.com/…" as our host.
  const host = strictHttpsHost(text);
  if (!host || !TSA_SITE_HOSTS.includes(host)) return null;
  const m = /^\/pay\/([^/]+)\/?$/.exec(new URL(text).pathname);
  return m && isPaymentId(m[1]) ? { kind: 'payment', id: m[1].toLowerCase() } : null;
}
