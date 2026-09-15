// services/gasSponsorship.ts
//
// Gas-free Paycrest sells. The backend decides per order HOW the network fee
// is covered:
//
//   authorization  the user signs an EIP-3009 transferWithAuthorization and
//                  our relayer submits it — USDC, and USDT0 on Arbitrum
//   topup          we send just enough native gas, then the user sends as
//                  usual — tokens with no EIP-3009 (BSC, Polygon USDT)
//   none           self-pay, exactly as before sponsorship existed
//
// The app never trusts the offer blindly: the signed message's sender,
// recipient, amount, token contract, chain and decimals must all match the
// order this screen is showing and the app's own token allowlist, or nothing
// is signed.
import { ethers } from 'ethers';
import { CHAINS, type ChainKey } from '@/constants/chains';
import { paycrestRequest } from './paycrestApi';

export type SponsorMode = 'none' | 'authorization' | 'topup';

export interface SponsorRoute {
  chain: string;
  token: string;
  mode: SponsorMode;
}

export interface SponsorshipOffer {
  mode: SponsorMode;
  chain: string;
  token: string;
  tokenAddress?: string;
  decimals?: number;
  from?: string;
  to?: string;
  /** Token base units. */
  value?: string;
  validBefore?: number;
  domain?: { name: string; version: string; chainId: string; verifyingContract: string };
}

export async function getSponsoredRoutes(): Promise<SponsorRoute[]> {
  try {
    const data = await paycrestRequest<{ enabled: boolean; routes: SponsorRoute[] }>('/sponsorship');
    return data?.enabled ? (data.routes ?? []) : [];
  } catch {
    // Off, an older server, or no network: all mean "pay your own gas".
    return [];
  }
}

export function isGasSponsored(routes: SponsorRoute[], chain: string, token: string): boolean {
  return routes.some(
    (r) =>
      r.mode !== 'none' &&
      r.chain.toLowerCase() === chain.toLowerCase() &&
      r.token.toUpperCase() === token.toUpperCase(),
  );
}

export function getSponsorshipOffer(orderId: string): Promise<SponsorshipOffer> {
  return paycrestRequest<SponsorshipOffer>(`/orders/${orderId}/sponsorship`);
}

export function relayAuthorization(
  orderId: string,
  body: { validAfter: number; validBefore: number; nonce: string; signature: string },
): Promise<{ txHash?: string; alreadyUsed: boolean }> {
  return paycrestRequest(`/orders/${orderId}/relay-authorization`, {
    method: 'POST',
    body: JSON.stringify(body),
  });
}

export function requestGasTopUp(
  orderId: string,
): Promise<{ txHash?: string; amountWei?: string; skipped: boolean }> {
  return paycrestRequest(`/orders/${orderId}/gas-topup`, { method: 'POST' });
}

export const TRANSFER_WITH_AUTHORIZATION_TYPES = {
  TransferWithAuthorization: [
    { name: 'from', type: 'address' },
    { name: 'to', type: 'address' },
    { name: 'value', type: 'uint256' },
    { name: 'validAfter', type: 'uint256' },
    { name: 'validBefore', type: 'uint256' },
    { name: 'nonce', type: 'bytes32' },
  ],
};

export function randomAuthorizationNonce(): string {
  return ethers.hexlify(ethers.randomBytes(32));
}

const sameAddress = (a?: string, b?: string) => !!a && !!b && a.toLowerCase() === b.toLowerCase();

// The only contracts Phase 1 relays (EIP-3009, verified on-chain), keyed
// chain:TOKEN. The server's offer must agree with these; a server override of
// any of them fails closed here, so nothing is signed.
const AUTHORIZATION_TOKENS: Record<string, { address: string; decimals: number }> = {
  'base:USDC': { address: '0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913', decimals: 6 },
  'arbitrum:USDC': { address: '0xaf88d065e77c8cC2239327C5EDb3A432268e5831', decimals: 6 },
  'arbitrum:USDT': { address: '0xFd086bC7CD5C481DCC9C85ebE478A1C0b69FCbb9', decimals: 6 },
  'polygon:USDC': { address: '0x3c499c542cEF5E3811e1192ce70d8cC03d5c3359', decimals: 6 },
};

export function buildTransferAuthorization(
  offer: SponsorshipOffer,
  order: { walletAddress: string; depositAddress?: string; tokenAmount?: string; chain: string; token: string },
  nonce: string,
) {
  if (
    offer.mode !== 'authorization' ||
    !offer.domain ||
    !offer.from ||
    !offer.to ||
    !offer.value ||
    !offer.validBefore ||
    offer.decimals == null
  ) {
    throw new Error('This order cannot be sent gas-free.');
  }
  const expected = AUTHORIZATION_TOKENS[`${order.chain.toLowerCase()}:${order.token.toUpperCase()}`];
  if (!expected) {
    throw new Error('This order cannot be sent gas-free.');
  }
  if (
    Number(offer.domain.chainId) !== CHAINS[order.chain.toLowerCase() as ChainKey]?.chainId ||
    !sameAddress(offer.domain.verifyingContract, expected.address) ||
    (offer.tokenAddress != null && !sameAddress(offer.tokenAddress, expected.address)) ||
    offer.decimals !== expected.decimals
  ) {
    throw new Error("The token details don't match this order. Nothing was sent.");
  }
  // A signed authorization is spendable by anyone who holds it, so it must
  // pay THIS order's deposit address from THIS wallet — checked here against
  // what the screen shows, not only on the server that issued the offer.
  if (!sameAddress(offer.from, order.walletAddress) || !sameAddress(offer.to, order.depositAddress)) {
    throw new Error("The deposit details don't match this order. Nothing was sent.");
  }
  if (!order.tokenAmount || ethers.parseUnits(order.tokenAmount, expected.decimals).toString() !== offer.value) {
    throw new Error("The amount doesn't match this order. Nothing was sent.");
  }
  if (!/^0x[0-9a-fA-F]{64}$/.test(nonce)) {
    throw new Error('Invalid authorization nonce.');
  }
  return {
    domain: {
      name: offer.domain.name,
      version: offer.domain.version,
      chainId: Number(offer.domain.chainId),
      verifyingContract: offer.domain.verifyingContract,
    },
    types: TRANSFER_WITH_AUTHORIZATION_TYPES,
    message: {
      from: offer.from,
      to: offer.to,
      value: offer.value,
      validAfter: '0',
      validBefore: String(offer.validBefore),
      nonce,
    },
  };
}
