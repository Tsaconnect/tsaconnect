// Business values in the words merchants read.
import { naira } from '@/lib/format';
import type { PayStatus } from '@/components/ui/status-pill';
import type { Bank, OwnMerchant, Payment, SettlementOption } from './api';

/** What the merchant receives is only real once the money is on its way. */
export const youReceive = (p: Payment) => (p.status === 'succeeded' || p.status === 'processing' ? naira(p.merchantNet) : '—');

/** A payment link's states in the words of the links page's filters. */
export const LINK_STATUS_LABELS = { pending: 'Awaiting payment', succeeded: 'Paid' } as const;

/** The status pill's state: a link its owner cancelled reads Cancelled, not Expired. */
export const statusOf = (p: Payment): PayStatus => (p.cancelled ? 'cancelled' : p.status);

/**
 * What a shared payment link says: "Sky Air Ltd is requesting ₦5,050.51 for … Pay here: …" —
 * the total the customer is charged, fee included, so the message matches the checkout.
 */
export const linkMessage = (businessName: string, p: Payment) =>
  `${businessName} is requesting ${naira(p.customerTotal)} for ${p.description}. Pay here: ${p.checkoutUrl}`;

/** WhatsApp's share page with the message filled in; the sender picks the chat. */
export const whatsAppUrl = (businessName: string, p: Payment) => `https://wa.me/?text=${encodeURIComponent(linkMessage(businessName, p))}`;

const CHAINS: Record<string, string> = {
  base: 'Base', bsc: 'BNB Chain', ethereum: 'Ethereum', polygon: 'Polygon', arbitrum: 'Arbitrum',
  optimism: 'Optimism', avalanche: 'Avalanche', sonic: 'Sonic', linea: 'Linea',
};

/** "base" → "Base"; an unlisted chain key is capitalised. */
export const chainLabel = (chain: string) => CHAINS[chain] ?? chain.charAt(0).toUpperCase() + chain.slice(1);

/** A select option's value and label: "USDC:8453", "USDC on Base". */
export const optionValue = (o: SettlementOption) => `${o.symbol}:${o.chainId}`;
export const optionLabel = (o: SettlementOption) => `${o.symbol} on ${chainLabel(o.chain)}`;

/** A wallet as the owner should compare it: first six and last four (0x5290…9EE7). */
export const shortAddress = (a: string) => (a.length > 12 ? `${a.slice(0, 6)}…${a.slice(-4)}` : a);

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
