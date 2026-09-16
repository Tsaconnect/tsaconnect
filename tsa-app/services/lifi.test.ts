import {
  getLiFiTokens, getLiFiQuote, getLiFiChains, buildERC20ApproveCalldata, settleSwapFee,
  isBtcLiFiChain, isNonEVMLiFiChain, SOLANA_LIFI_CHAIN_ID, BTC_LIFI_CHAIN_ID,
} from './lifi';

jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn().mockResolvedValue('Bearer test-token'),
}));

const mockGetNetwork = jest.fn(() => 'mainnet');
jest.mock('../hooks/useNetwork', () => ({
  getNetwork: () => mockGetNetwork(),
}));

const mockFetch = jest.fn();
global.fetch = mockFetch;

const MOCK_QUOTE = {
  toAmount: '1000000000000000000',
  toAmountUSD: '1000',
  tool: '1inch',
  transactionRequest: {
    to: '0xrouter',
    data: '0xdeadbeef',
    value: '0x0',
    chainId: 56,
    gasLimit: '200000',
    gasPrice: '5000000000',
  },
};

beforeEach(() => {
  mockFetch.mockReset();
  mockGetNetwork.mockReturnValue('mainnet');
});

describe('getLiFiChains', () => {
  const CHAINS_BODY = {
    chains: [
      { id: 56, name: 'BSC', key: 'bsc', coin: 'BNB', chainType: 'EVM' },
      { id: SOLANA_LIFI_CHAIN_ID, name: 'Solana', key: 'sol', coin: 'SOL', chainType: 'SVM' },
      { id: BTC_LIFI_CHAIN_ID, name: 'Bitcoin', key: 'btc', coin: 'BTC', chainType: 'UTXO' },
    ],
  };

  it('fetches through the backend proxy and includes Solana and Bitcoin on mainnet', async () => {
    mockFetch.mockResolvedValueOnce({ ok: true, json: async () => CHAINS_BODY });
    const chains = await getLiFiChains();
    expect(mockFetch).toHaveBeenCalledWith(expect.stringContaining('/swap/lifi/chains'), expect.any(Object));
    expect(chains.map(c => c.id)).toEqual([56, SOLANA_LIFI_CHAIN_ID, BTC_LIFI_CHAIN_ID]);
  });

  it('hides both non-EVM chains in testnet mode (swaps are mainnet-only)', async () => {
    mockGetNetwork.mockReturnValue('testnet');
    mockFetch.mockResolvedValueOnce({ ok: true, json: async () => CHAINS_BODY });
    const chains = await getLiFiChains();
    expect(chains.map(c => c.id)).toEqual([56]);
  });

  // The Bitcoin chain id is a LiFi-side constant, so trust LiFi's own answer
  // over ours: a UTXO chain we did not know about must still route down the
  // Bitcoin signer rather than be treated as an EVM chain.
  it('learns UTXO chain ids from the chain list', async () => {
    const UNKNOWN_UTXO = 20000000000009;
    expect(isBtcLiFiChain(UNKNOWN_UTXO)).toBe(false);
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ chains: [{ id: UNKNOWN_UTXO, name: 'Bitcoin', key: 'btc', coin: 'BTC', chainType: 'UTXO' }] }),
    });
    await getLiFiChains();
    expect(isBtcLiFiChain(UNKNOWN_UTXO)).toBe(true);
    expect(isNonEVMLiFiChain(UNKNOWN_UTXO)).toBe(true);
  });
});

describe('settleSwapFee', () => {
  const params = {
    quoteId: '0xswaphash',
    fromChainId: 42161,
    toChainId: 1,
    fromToken: '0xaf88d065e77c8cC2239327C5EDb3A432268e5831',
    toToken: '0x0000000000000000000000000000000000000000',
    fromAmount: '1000000000',
    toAmount: '250957',
    fromSymbol: 'USDC',
    fromDecimals: 6,
    fromAddress: '0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045',
    toAddress: '0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045',
    feature: 'allswap' as const,
  };

  it('reports the executed route and never claims a fee figure', async () => {
    mockFetch.mockResolvedValueOnce({ ok: true, text: async () => '{}' });
    await settleSwapFee(params);

    const [url, init] = mockFetch.mock.calls[0];
    expect(url).toContain('/swap/lifi/settle');
    expect(init.method).toBe('POST');
    const body = JSON.parse(init.body);
    expect(body).toMatchObject({
      quoteId: '0xswaphash',
      chainId: 42161,
      toChainId: 1,
      fromToken: params.fromToken,
      fromAmount: '1000000000',
      fromSymbol: 'USDC',
      fromDecimals: 6,
      feature: 'allswap',
    });
    // LiFi refuses to quote without fromAddress, so the backend cannot verify
    // the fee without the route's endpoints.
    expect(body.fromAddress).toBe(params.fromAddress);
    expect(body.toAddress).toBe(params.toAddress);
    // The server recomputes the fee from a fresh quote; a number from here is
    // precisely what it is built not to trust.
    expect(body).not.toHaveProperty('clientFeeUsd');
  });

  it('throws on a rejected settle so the caller can log it', async () => {
    mockFetch.mockResolvedValueOnce({ ok: false, status: 422, text: async () => 'fee deviation' });
    await expect(settleSwapFee(params)).rejects.toThrow(/422/);
  });
});

describe('chain family helpers', () => {
  it('separates EVM chains from the families that sign off-chain', () => {
    expect(isBtcLiFiChain(BTC_LIFI_CHAIN_ID)).toBe(true);
    expect(isBtcLiFiChain(56)).toBe(false);
    expect(isNonEVMLiFiChain(SOLANA_LIFI_CHAIN_ID)).toBe(true);
    expect(isNonEVMLiFiChain(BTC_LIFI_CHAIN_ID)).toBe(true);
    expect(isNonEVMLiFiChain(1)).toBe(false);
  });
});

describe('getLiFiTokens', () => {
  it('returns token list on success', async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ tokens: { '56': [{ address: '0xabc', symbol: 'USDT', name: 'Tether', decimals: 6, logoURI: '' }] } }),
    });
    const tokens = await getLiFiTokens(56);
    expect(tokens).toHaveLength(1);
    expect(tokens[0].symbol).toBe('USDT');
    expect(mockFetch).toHaveBeenCalledWith(expect.stringContaining('/swap/lifi/tokens?chainId=56'), expect.any(Object));
  });

  it('throws on non-ok response', async () => {
    mockFetch.mockResolvedValueOnce({ ok: false, json: async () => ({ message: 'error' }) });
    await expect(getLiFiTokens(56)).rejects.toThrow();
  });

  it('returns empty array when chain tokens missing from response', async () => {
    mockFetch.mockResolvedValueOnce({ ok: true, json: async () => ({ tokens: {} }) });
    const tokens = await getLiFiTokens(56);
    expect(tokens).toEqual([]);
  });
});

describe('getLiFiQuote', () => {
  it('returns quote with transactionRequest on success', async () => {
    mockFetch.mockResolvedValueOnce({ ok: true, json: async () => MOCK_QUOTE });
    const quote = await getLiFiQuote({ fromChain: 56, toChain: 1, fromToken: '0xabc', toToken: '0xdef', fromAmount: '1000000', fromAddress: '0x123' });
    expect(quote.transactionRequest.to).toBe('0xrouter');
    expect(quote.approvalAddress).toBeUndefined();
  });

  it('exposes approvalAddress and approvalToken when present', async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ ...MOCK_QUOTE, estimate: { approvalAddress: '0xspender' }, action: { fromToken: { address: '0xtoken' } } }),
    });
    const quote = await getLiFiQuote({ fromChain: 56, toChain: 1, fromToken: '0xabc', toToken: '0xdef', fromAmount: '1000000', fromAddress: '0x123' });
    expect(quote.approvalAddress).toBe('0xspender');
    expect(quote.approvalToken).toBe('0xtoken');
  });

  it('throws on non-ok response', async () => {
    mockFetch.mockResolvedValueOnce({ ok: false, json: async () => ({ message: 'No route found' }) });
    await expect(getLiFiQuote({ fromChain: 56, toChain: 1, fromToken: '0xabc', toToken: '0xdef', fromAmount: '1000000', fromAddress: '0x123' })).rejects.toThrow('No route found');
  });

  it('forwards toAddress for cross-VM routes and omits it otherwise', async () => {
    mockFetch.mockResolvedValueOnce({ ok: true, json: async () => MOCK_QUOTE });
    await getLiFiQuote({
      fromChain: SOLANA_LIFI_CHAIN_ID, toChain: 42161, fromToken: 'SOL', toToken: '0xdef',
      fromAmount: '1000000000', fromAddress: 'So1anaAddr', toAddress: '0xEvmRecipient',
    });
    expect(mockFetch.mock.calls[0][0]).toContain('toAddress=0xEvmRecipient');

    mockFetch.mockResolvedValueOnce({ ok: true, json: async () => MOCK_QUOTE });
    await getLiFiQuote({ fromChain: 56, toChain: 1, fromToken: '0xabc', toToken: '0xdef', fromAmount: '1', fromAddress: '0x123' });
    expect(mockFetch.mock.calls[1][0]).not.toContain('toAddress');
  });
});

describe('buildERC20ApproveCalldata', () => {
  it('starts with approve selector 0x095ea7b3', () => {
    const data = buildERC20ApproveCalldata('0x1234567890123456789012345678901234567890', '1000000');
    expect(data.startsWith('0x095ea7b3')).toBe(true);
  });

  it('encodes spender address in calldata', () => {
    const spender = '0x1234567890123456789012345678901234567890';
    const data = buildERC20ApproveCalldata(spender, '1000000');
    expect(data.toLowerCase()).toContain('1234567890123456789012345678901234567890');
  });
});
