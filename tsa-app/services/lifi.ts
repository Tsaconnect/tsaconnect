import { ethers } from 'ethers';
import { API_BASE_URL } from '../constants/api/config';
import { getAuthHeaders } from './walletApi';

export interface LiFiToken {
  address: string;
  symbol: string;
  name: string;
  decimals: number;
  logoURI: string;
  priceUSD?: string;
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
}

export async function getLiFiTokens(chainId: number): Promise<LiFiToken[]> {
  const headers = await getAuthHeaders();
  const res = await fetch(`${API_BASE_URL}/swap/lifi/tokens?chainId=${chainId}`, { headers });
  if (!res.ok) throw new Error('Failed to fetch token list');
  const data = await res.json();
  const chainTokens = data?.tokens?.[String(chainId)];
  return Array.isArray(chainTokens) ? chainTokens : [];
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

  return {
    toAmount: data.toAmount,
    toAmountUSD: data.toAmountUSD,
    estimatedDuration: data.estimate?.executionDuration ?? 0,
    gasCostUSD: data.estimate?.gasCosts?.[0]?.amountUSD ?? '0',
    tool: data.toolDetails?.name ?? data.tool ?? 'LiFi',
    transactionRequest: data.transactionRequest,
    approvalAddress: data.estimate?.approvalAddress || undefined,
    approvalToken: data.action?.fromToken?.address || undefined,
  };
}

/** ABI-encodes approve(address,uint256) calldata for ERC-20 token approval. */
export function buildERC20ApproveCalldata(spender: string, amount: string): string {
  const iface = new ethers.Interface(['function approve(address spender, uint256 amount)']);
  return iface.encodeFunctionData('approve', [spender, BigInt(amount)]);
}
