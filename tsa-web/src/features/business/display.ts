// Business values in the words merchants read.
import { naira } from '@/lib/format';
import type { PayStatus } from '@/components/ui/status-pill';
import type { Bank, Mode, NotTakingReason, OpenLink, OwnMerchant, Payment, SettlementOption } from './api';

/** What the merchant receives is only real once the money is on its way. */
export const youReceive = (p: Payment) => (p.status === 'succeeded' || p.status === 'processing' ? naira(p.merchantNet) : '—');

/** Live payments are off for this business right now (an older API only sends the merchant's own switch). */
export const isLiveOff = (m: OwnMerchant, mode: Mode) => mode === 'live' && !(m.liveAllowed ?? m.liveEnabled);

type AmountRange = Pick<OpenLink, 'minAmount' | 'maxAmount' | 'merchantMinAmount' | 'merchantMaxAmount'>;

/**
 * What a customer may pay through a reusable link, as the server works it out
 * (payOpenLinkBounds): the link's own range narrowed by the business's limits as
 * they are now, a limit of 0 being the business's own — "From ₦1,000.00 to ₦5,000,000.00".
 */
export function openAmountRule(l: AmountRange): string {
  const min = Math.max(l.minAmount, l.merchantMinAmount);
  // An older API sends no business limits (0): then only the link's own maximum is known.
  const max = l.merchantMaxAmount ? Math.min(l.maxAmount || l.merchantMaxAmount, l.merchantMaxAmount) : l.maxAmount;
  if (min && max) return `From ${naira(min)} to ${naira(max)}`;
  if (min) return `From ${naira(min)}`;
  return max ? `Up to ${naira(max)}` : 'Any amount';
}

const NOT_TAKING: Record<NotTakingReason, string> = {
  paused: 'Paused',
  suspended: 'Your account is suspended',
  live_off: "Live payments aren't on",
  no_payout: 'Set your payout account',
  range_outside_limits: 'Your amount range no longer fits your limits — edit the link',
};

/** Whether a customer can pay a reusable link now, and the reason in the owner's words when not. */
export function openLinkTaking(l: OpenLink): { taking: boolean; reason?: string } {
  const taking = l.takingPayments ?? l.active;
  if (taking) return { taking };
  return { taking, reason: l.notTakingReason ? NOT_TAKING[l.notTakingReason] : NOT_TAKING.paused };
}

/** What a shared reusable link says — the same words as the app: "Pay Mama Ngozi Store — Shop 12: https://…". */
export const openLinkMessage = (businessName: string, l: OpenLink) => `Pay ${businessName} — ${l.label}: ${l.url}`;

/** "https://tsaconnectworld.com/l/mama-ngozi-7k3q" → "tsaconnectworld.com/l/mama-ngozi-7k3q", for print. */
export const shortUrl = (url: string) => url.replace(/^https?:\/\//, '');

/** A payment link's states in the words of the links page's filters. */
export const LINK_STATUS_LABELS = { pending: 'Awaiting payment', succeeded: 'Paid' } as const;

/**
 * What a payment was for, and the customer's note. A reusable-link payment is for
 * its link (linkLabel); anything the customer typed there is their note, never
 * the owner's description.
 */
export function paymentFor(p: Payment): { what: string; note?: string } {
  if (p.source !== 'open') return { what: p.description };
  return { what: p.linkLabel ?? '', note: p.description || undefined };
}

/** One line for a table: "Sky Air — Ikeja desk · Note: two bags". */
export function paymentForLine(p: Payment): string {
  const { what, note } = paymentFor(p);
  return [what, note && `Note: ${note}`].filter(Boolean).join(' · ');
}

/** The status pill's state: a link its owner cancelled reads Cancelled, not Expired. */
export const statusOf = (p: Payment): PayStatus => (p.cancelled ? 'cancelled' : p.status);

/**
 * What a shared payment link says: "Sky Air Ltd is requesting ₦5,050.51 for … Pay here: …" —
 * the total the customer is charged, fee included, so the message matches the checkout.
 */
export const linkMessage = (businessName: string, p: Payment) =>
  `${businessName} is requesting ${naira(p.customerTotal)} for ${p.description}. Pay here: ${p.checkoutUrl}`;

/** The QR card for a one-time link: the total the customer pays, and what for. */
export const linkCard = (businessName: string, p: Payment) =>
  ({ businessName, headline: naira(p.customerTotal), detail: p.description, test: p.mode === 'test' });

/** The QR card for a reusable link: its label, and the amounts it takes. */
export const openLinkCard = (businessName: string, l: OpenLink) =>
  ({ businessName, headline: l.label, detail: openAmountRule(l), test: l.mode === 'test' });

/** WhatsApp's share page with the message filled in; the sender picks the chat. */
export const whatsAppUrl = (message: string) => `https://wa.me/?text=${encodeURIComponent(message)}`;

// The networks a TSA Pay payout or refund can use: name, chain id and block explorer.
const CHAINS: Record<string, { name: string; id: number; explorer: string }> = {
  base: { name: 'Base', id: 8453, explorer: 'https://basescan.org' },
  bsc: { name: 'BNB Chain', id: 56, explorer: 'https://bscscan.com' },
  ethereum: { name: 'Ethereum', id: 1, explorer: 'https://etherscan.io' },
  polygon: { name: 'Polygon', id: 137, explorer: 'https://polygonscan.com' },
  arbitrum: { name: 'Arbitrum', id: 42161, explorer: 'https://arbiscan.io' },
  optimism: { name: 'Optimism', id: 10, explorer: 'https://optimistic.etherscan.io' },
  avalanche: { name: 'Avalanche', id: 43114, explorer: 'https://snowtrace.io' },
  sonic: { name: 'Sonic', id: 146, explorer: 'https://sonicscan.org' },
  linea: { name: 'Linea', id: 59144, explorer: 'https://lineascan.build' },
};
const chainById = (id: number) => Object.values(CHAINS).find((c) => c.id === id);

/** "base" → "Base"; an unlisted chain key is capitalised. */
export const chainLabel = (chain: string) => CHAINS[chain]?.name ?? chain.charAt(0).toUpperCase() + chain.slice(1);

/** 56 → "BNB Chain"; an unlisted id → "chain 56". */
export const chainLabelById = (id: number) => chainById(id)?.name ?? `chain ${id}`;

// A hash lands in a URL path: anything but a plain EVM hash ("../address/0x…") could send
// the link elsewhere on a trusted explorer. The same rule as the app's txExplorerUrl.
const EVM_TX_HASH = /^0x[0-9a-fA-F]{64}$/;

/** A transaction's explorer page, or undefined for an unknown chain or anything that isn't a hash. */
export function txExplorerUrl(hash: string, chainId: number): string | undefined {
  const chain = chainById(chainId);
  return chain && EVM_TX_HASH.test(hash) ? `${chain.explorer}/tx/${hash}` : undefined;
}

/** A select option's value and label: "USDC:8453", "USDC on Base". */
export const optionValue = (o: SettlementOption) => `${o.symbol}:${o.chainId}`;
export const optionLabel = (o: SettlementOption) => `${o.symbol} on ${chainLabel(o.chain)}`;

/** A wallet as the owner should compare it: first six and last four (0x5290…9EE7). */
export const shortAddress = (a: string) => (a.length > 12 ? `${a.slice(0, 6)}…${a.slice(-4)}` : a);

/** A transaction link's accessible name: each one in a list reads apart ("View transaction 0xabab…abab"). */
export const txLinkLabel = (hash: string) => `View transaction ${shortAddress(hash)}`;

export const bankName = (code: string | undefined, banks: Bank[] | undefined) => banks?.find((b) => b.code === code)?.name;

/** "USDC on Base to 0x5290…9EE7" — the chain named from the settlement options when known. */
export function cryptoDestination(m: OwnMerchant, options: SettlementOption[] | undefined): string {
  const chain = options?.find((o) => o.chainId === m.chainId)?.chain;
  const on = chain ? chainLabel(chain) : `chain ${m.chainId}`;
  return `${m.tokenSymbol} on ${on} to ${shortAddress(m.walletAddress ?? '')}`;
}

/** "Access Bank · ******3210" (the bank's name once the directory is loaded). */
export function bankDestination(m: OwnMerchant, banks: Bank[] | undefined): string {
  return [bankName(m.bankCode, banks), m.accountNumber].filter(Boolean).join(' · ');
}
