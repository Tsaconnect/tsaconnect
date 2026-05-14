import { ethers } from 'ethers';
import { API_BASE_URL } from '../constants/api/config';
import { getAuthHeaders } from './walletApi';

export interface LiFiChain {
  id: number;
  name: string;
  key: string;
  coin: string;
  nativeToken?: { logoURI?: string };
}

export interface LiFiToken {
  address: string;
  symbol: string;
  name: string;
  decimals: number;
  logoURI: string;
  priceUSD?: string;
  chainId?: number;
}

export interface LiFiQuoteParams {
  fromChain: number;
  toChain: number;
  fromToken: string;
  toToken: string;
  fromAmount: string;
  fromAddress: string;
  slippage?: string;
}

export interface LiFiTransactionRequest {
  to: string;
  data: string;
  value: string;
  chainId: number;
  gasLimit: string;
  gasPrice?: string;
  maxFeePerGas?: string;
  maxPriorityFeePerGas?: string;
}

export interface LiFiQuote {
  toAmount: string;
  toAmountUSD: string;
  estimatedDuration: number;
  gasCostUSD: string;
  tool: string;
  transactionRequest: LiFiTransactionRequest;
  approvalAddress?: string;
  approvalToken?: string;
  /** Unix timestamp (seconds) after which this quote is stale */
  validUntil?: number;
}

export async function getLiFiChains(): Promise<LiFiChain[]> {
  const res = await fetch('https://li.quest/v1/chains?chainTypes=EVM');
  if (!res.ok) throw new Error('Failed to fetch LiFi chains');
  const data = await res.json();
  return Array.isArray(data?.chains) ? data.chains : [];
}

export async function getLiFiTokens(chainId: number): Promise<LiFiToken[]> {
  const headers = await getAuthHeaders();
  const res = await fetch(`${API_BASE_URL}/swap/lifi/tokens?chainId=${chainId}`, { headers });
  if (!res.ok) throw new Error('Failed to fetch token list');
  const data = await res.json();
  const chainTokens = data?.tokens?.[String(chainId)];
  return Array.isArray(chainTokens) ? chainTokens : [];
}

/** Fetch tokens for multiple chains in parallel, stamping chainId on each token. */
export async function getLiFiAllTokens(chainIds: number[]): Promise<LiFiToken[]> {
  const headers = await getAuthHeaders();
  const results = await Promise.allSettled(
    chainIds.map(chainId =>
      fetch(`${API_BASE_URL}/swap/lifi/tokens?chainId=${chainId}`, { headers })
        .then(r => r.json())
        .then(data => {
          const tokens: LiFiToken[] = data?.tokens?.[String(chainId)] ?? [];
          return tokens.map(t => ({ ...t, chainId }));
        })
        .catch(() => [] as LiFiToken[])
    )
  );
  return results
    .filter(r => r.status === 'fulfilled')
    .flatMap(r => (r as PromiseFulfilledResult<LiFiToken[]>).value);
}

/** Look up a single token by contract address. Returns null if not found. */
export async function getLiFiToken(chainId: number, address: string): Promise<LiFiToken | null> {
  const headers = await getAuthHeaders();
  const res = await fetch(`${API_BASE_URL}/swap/lifi/token?chain=${chainId}&token=${address}`, { headers });
  if (!res.ok) return null;
  const data = await res.json();
  return data?.address ? (data as LiFiToken) : null;
}

export async function getLiFiQuote(params: LiFiQuoteParams): Promise<LiFiQuote> {
  const headers = await getAuthHeaders();
  const qs = new URLSearchParams({
    fromChain: String(params.fromChain),
    toChain: String(params.toChain),
    fromToken: params.fromToken,
    toToken: params.toToken,
    fromAmount: params.fromAmount,
    fromAddress: params.fromAddress,
    ...(params.slippage ? { slippage: params.slippage } : {}),
  });

  const res = await fetch(`${API_BASE_URL}/swap/lifi/quote?${qs.toString()}`, { headers });
  const data = await res.json();
  if (!res.ok) throw new Error(data?.message || 'Failed to fetch quote');

  // LiFi v1: toAmount lives in estimate, not at the top level
  return {
    toAmount: data.estimate?.toAmount ?? data.toAmount ?? '0',
    toAmountUSD: data.estimate?.toAmountUSD ?? data.toAmountUSD ?? '0',
    estimatedDuration: data.estimate?.executionDuration ?? 0,
    gasCostUSD: data.estimate?.gasCosts?.[0]?.amountUSD ?? '0',
    tool: data.toolDetails?.name ?? data.tool ?? 'LiFi',
    transactionRequest: data.transactionRequest,
    approvalAddress: data.estimate?.approvalAddress || undefined,
    approvalToken: data.action?.fromToken?.address || undefined,
    validUntil: data.estimate?.validUntil ?? undefined,
  };
}

/** ABI-encodes approve(address,uint256) calldata for ERC-20 token approval. */
export function buildERC20ApproveCalldata(spender: string, amount: string): string {
  const iface = new ethers.Interface(['function approve(address spender, uint256 amount)']);
  return iface.encodeFunctionData('approve', [spender, BigInt(amount)]);
}
