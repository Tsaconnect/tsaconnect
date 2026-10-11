// services/__tests__/escrowFunding.test.ts
//
// Covers the money-moving core of "hide the pool" (spec 2026-06-05): the
// ensureEscrowFunding branch logic that runs inline at ad-publish time. This is
// the riskiest new surface in the change, so the tests exercise the REAL module
// (BigInt shortfall math, already-funded short-circuit, gas abort, allowance
// skip, approve->waitMined->deposit ordering, and the confirm-fail stash) with
// only its collaborators (poolApi + wallet helpers + the pending store) mocked.
//
// Mapping to the spec's Testing section:
//   - shortfall == 0           -> {status:'already-funded'}, no prepare/broadcast
//   - shortfall  > 0           -> approves+deposits then confirm -> {status:'funded'}
//   - allowance >= shortfall   -> skips prepareApprove, still deposits
//   - allowance  < shortfall   -> approves shortfall, WAITS mined, then deposits
//   - confirm fails            -> {status:'pending-confirm'} + matching addPending
//   - ensureGas false          -> {status:'aborted'}
//   - chainId <= 0             -> throws (never broadcast on an unresolved chain)

// ── Mocks ───────────────────────────────────────────────────────────────────
// All collaborators are mocked; the module under test runs for real so its
// branch decisions and ordering are what's being asserted.

const mockBalance = jest.fn();
const mockAllowance = jest.fn();
const mockPrepareApprove = jest.fn();
const mockPrepareDeposit = jest.fn();
const mockConfirmDeposit = jest.fn();

jest.mock('../p2p', () => ({
  poolApi: {
    balance: (...a: any[]) => mockBalance(...a),
    allowance: (...a: any[]) => mockAllowance(...a),
    prepareApprove: (...a: any[]) => mockPrepareApprove(...a),
    prepareDeposit: (...a: any[]) => mockPrepareDeposit(...a),
    confirmDeposit: (...a: any[]) => mockConfirmDeposit(...a),
  },
  // Deterministic, side-effect-free stand-ins for the two pure adapters.
  p2pChainKey: (chainId: number) => `chain-${chainId}`,
  unsignedTxToTxLike: (tx: any) => ({ __txLike: true, ...tx }),
}));

const mockSignTransaction = jest.fn();
const mockBroadcastTransaction = jest.fn();
const mockEnsureGas = jest.fn();
const mockWaitForTransaction = jest.fn();

jest.mock('../wallet', () => ({
  signTransaction: (...a: any[]) => mockSignTransaction(...a),
  broadcastTransaction: (...a: any[]) => mockBroadcastTransaction(...a),
  ensureGas: (...a: any[]) => mockEnsureGas(...a),
  getProvider: () => ({ waitForTransaction: (...a: any[]) => mockWaitForTransaction(...a) }),
}));

const mockEnsureP2PGas = jest.fn();
jest.mock('../p2pGas', () => ({ ensureP2PGas: (...a: any[]) => mockEnsureP2PGas(...a) }));

const mockAddPending = jest.fn();
jest.mock('../p2pPending', () => ({
  addPending: (...a: any[]) => mockAddPending(...a),
}));

// eslint-disable-next-line import/first -- jest.mock factories must hoist above this import.
import { ensureEscrowFunding, type EnsureFundingParams } from '../escrowFunding';

const TOKEN = 'USDT';
const CHAIN_ID = 56; // BSC: a real (>0), non-Sonic chain.
const WALLET = '0xSELLER';

function params(over: Partial<EnsureFundingParams> = {}): EnsureFundingParams {
  return {
    token: TOKEN,
    chainId: CHAIN_ID,
    amountBaseUnits: '100',
    walletAddress: WALLET,
    ...over,
  };
}

// Convenience: arrange a happy on-chain world (gas ok, signs/broadcasts/mines).
function arrangeHappyChain() {
  mockEnsureGas.mockResolvedValue(true);
  mockSignTransaction.mockResolvedValue('0xsigned');
  mockBroadcastTransaction.mockImplementation(async () => ({ hash: '0xdeposit' }));
  mockWaitForTransaction.mockResolvedValue({ status: 1 });
  mockPrepareApprove.mockResolvedValue({ success: true, data: { unsignedTx: { to: '0xtok' } } });
  mockPrepareDeposit.mockResolvedValue({ success: true, data: { unsignedTx: { to: '0xescrow' } } });
}

beforeEach(() => {
  jest.clearAllMocks();
});

// waitMined() races provider.waitForTransaction against a 90s setTimeout. When
// the wait wins (every approve path here), that timer is left pending and would
// otherwise hold the Jest process open ("did not exit"). Fake timers make the
// dangling timeout inert without changing production code.
beforeAll(() => {
  jest.useFakeTimers();
});
afterAll(() => {
  jest.useRealTimers();
});

describe('ensureEscrowFunding — chain-resolution guard', () => {
  it('throws when chainId <= 0 (never broadcast on an unresolved/defaulted chain)', async () => {
    await expect(ensureEscrowFunding(params({ chainId: 0 }))).rejects.toThrow(/resolved chain id/i);
    await expect(ensureEscrowFunding(params({ chainId: -1 }))).rejects.toThrow(/resolved chain id/i);
    // Guard runs before anything else — no balance read, no broadcast.
    expect(mockBalance).not.toHaveBeenCalled();
    expect(mockBroadcastTransaction).not.toHaveBeenCalled();
  });
});

describe('ensureEscrowFunding — already covered (shortfall == 0)', () => {
  it('returns already-funded with NO prepare/sign/broadcast/confirm when available >= need', async () => {
    mockBalance.mockResolvedValue({ success: true, data: { available: '100', locked: '0' } });

    const res = await ensureEscrowFunding(params({ amountBaseUnits: '100' }));

    expect(res).toEqual({ status: 'already-funded' });
    // No gas check, no allowance read, no money moved.
    expect(mockEnsureGas).not.toHaveBeenCalled();
    expect(mockAllowance).not.toHaveBeenCalled();
    expect(mockPrepareApprove).not.toHaveBeenCalled();
    expect(mockPrepareDeposit).not.toHaveBeenCalled();
    expect(mockBroadcastTransaction).not.toHaveBeenCalled();
    expect(mockConfirmDeposit).not.toHaveBeenCalled();
  });

  it('treats available > need as already-funded (overfunded pool)', async () => {
    mockBalance.mockResolvedValue({ success: true, data: { available: '500', locked: '0' } });
    const res = await ensureEscrowFunding(params({ amountBaseUnits: '100' }));
    expect(res).toEqual({ status: 'already-funded' });
    expect(mockBroadcastTransaction).not.toHaveBeenCalled();
  });
});

describe('ensureEscrowFunding — gas guard', () => {
  it('aborts (no broadcast) when ensureGas returns false', async () => {
    mockBalance.mockResolvedValue({ success: true, data: { available: '0', locked: '0' } });
    mockEnsureGas.mockResolvedValue(false);

    const res = await ensureEscrowFunding(params({ amountBaseUnits: '100' }));

    expect(res).toEqual({ status: 'aborted', reason: 'no gas' });
    // Abort happens before allowance read and before any tx.
    expect(mockAllowance).not.toHaveBeenCalled();
    expect(mockPrepareDeposit).not.toHaveBeenCalled();
    expect(mockBroadcastTransaction).not.toHaveBeenCalled();
  });

  it('collapses ensureGas (title,message) alerter into the single-string onGasAlert', async () => {
    mockBalance.mockResolvedValue({ success: true, data: { available: '0', locked: '0' } });
    // Drive the alerter ensureEscrowFunding passes in, to prove the bridge works.
    mockEnsureGas.mockImplementation(async (_chainKey: string, _addr: string, alert?: any) => {
      alert?.('Need gas', 'Top up BNB');
      return false;
    });
    const onGasAlert = jest.fn();

    await ensureEscrowFunding(params({ amountBaseUnits: '100', onGasAlert }));

    expect(onGasAlert).toHaveBeenCalledWith('Need gas: Top up BNB');
  });
});

describe('ensureEscrowFunding — allowance handling', () => {
  it('skips prepareApprove when allowance >= shortfall, still deposits + confirms', async () => {
    mockBalance.mockResolvedValue({ success: true, data: { available: '40', locked: '0' } });
    arrangeHappyChain();
    // shortfall = 100 - 40 = 60; allowance covers it.
    mockAllowance.mockResolvedValue({ success: true, data: { allowance: '60' } });
    mockConfirmDeposit.mockResolvedValue({ success: true });

    const res = await ensureEscrowFunding(params({ amountBaseUnits: '100' }));

    expect(res).toEqual({ status: 'funded', deposited: '60' });
    expect(mockPrepareApprove).not.toHaveBeenCalled();
    expect(mockWaitForTransaction).not.toHaveBeenCalled(); // no approve => no mine-wait
    // Deposit was prepared/confirmed for the exact shortfall.
    expect(mockPrepareDeposit).toHaveBeenCalledWith(TOKEN, '60', WALLET, CHAIN_ID);
    expect(mockConfirmDeposit).toHaveBeenCalledWith(TOKEN, '60', '0xdeposit', WALLET, CHAIN_ID);
  });

  it('approves the shortfall, waits for it to mine, THEN deposits when allowance < shortfall', async () => {
    mockBalance.mockResolvedValue({ success: true, data: { available: '40', locked: '0' } });
    arrangeHappyChain();
    mockAllowance.mockResolvedValue({ success: true, data: { allowance: '10' } }); // < 60
    mockConfirmDeposit.mockResolvedValue({ success: true });

    // Record call order across the three chain steps to prove approve mines
    // before the deposit broadcasts (the transferFrom-can't-race-approve invariant).
    const order: string[] = [];
    mockBroadcastTransaction.mockImplementation(async () => {
      order.push('broadcast');
      return { hash: order.filter((s) => s === 'broadcast').length === 1 ? '0xapprove' : '0xdeposit' };
    });
    mockWaitForTransaction.mockImplementation(async () => {
      order.push('waitMined');
      return { status: 1 };
    });
    mockPrepareDeposit.mockImplementation(async () => {
      order.push('prepareDeposit');
      return { success: true, data: { unsignedTx: { to: '0xescrow' } } };
    });

    const res = await ensureEscrowFunding(params({ amountBaseUnits: '100' }));

    expect(res).toEqual({ status: 'funded', deposited: '60' });
    expect(mockPrepareApprove).toHaveBeenCalledWith(TOKEN, '60', WALLET, CHAIN_ID);
    // approve broadcast -> mined -> deposit prepared -> deposit broadcast.
    expect(order).toEqual(['broadcast', 'waitMined', 'prepareDeposit', 'broadcast']);
    expect(mockConfirmDeposit).toHaveBeenCalledWith(TOKEN, '60', '0xdeposit', WALLET, CHAIN_ID);
  });

  it('stops before the deposit when the approve reverted on chain', async () => {
    mockBalance.mockResolvedValue({ success: true, data: { available: '0', locked: '0' } });
    arrangeHappyChain();
    mockAllowance.mockResolvedValue({ success: true, data: { allowance: '0' } });
    mockWaitForTransaction.mockResolvedValue({ status: 0 });

    await expect(ensureEscrowFunding(params({ amountBaseUnits: '100' }))).rejects.toThrow(/failed on chain/);
    expect(mockPrepareDeposit).not.toHaveBeenCalled();
  });

  it('treats a non-numeric allowance as 0 and runs the approve', async () => {
    mockBalance.mockResolvedValue({ success: true, data: { available: '0', locked: '0' } });
    arrangeHappyChain();
    mockAllowance.mockResolvedValue({ success: true, data: { allowance: 'not-a-number' } });
    mockConfirmDeposit.mockResolvedValue({ success: true });

    await ensureEscrowFunding(params({ amountBaseUnits: '100' }));

    expect(mockPrepareApprove).toHaveBeenCalledWith(TOKEN, '100', WALLET, CHAIN_ID);
    expect(mockWaitForTransaction).toHaveBeenCalled();
  });

  it('treats a failed allowance read as 0 and runs the approve', async () => {
    mockBalance.mockResolvedValue({ success: true, data: { available: '0', locked: '0' } });
    arrangeHappyChain();
    mockAllowance.mockResolvedValue({ success: false });
    mockConfirmDeposit.mockResolvedValue({ success: true });

    await ensureEscrowFunding(params({ amountBaseUnits: '100' }));

    expect(mockPrepareApprove).toHaveBeenCalledWith(TOKEN, '100', WALLET, CHAIN_ID);
  });
});

describe('ensureEscrowFunding — confirm outcomes', () => {
  it('returns funded on confirm success and does NOT stash a pending record', async () => {
    mockBalance.mockResolvedValue({ success: true, data: { available: '0', locked: '0' } });
    arrangeHappyChain();
    mockAllowance.mockResolvedValue({ success: true, data: { allowance: '1000' } });
    mockConfirmDeposit.mockResolvedValue({ success: true });

    const res = await ensureEscrowFunding(params({ amountBaseUnits: '100' }));

    expect(res).toEqual({ status: 'funded', deposited: '100' });
    expect(mockAddPending).not.toHaveBeenCalled();
  });

  it('on confirm failure returns pending-confirm AND stashes a matching addPending record', async () => {
    mockBalance.mockResolvedValue({ success: true, data: { available: '0', locked: '0' } });
    arrangeHappyChain();
    mockAllowance.mockResolvedValue({ success: true, data: { allowance: '1000' } });
    mockBroadcastTransaction.mockResolvedValue({ hash: '0xDEPOSIT_TX' });
    mockConfirmDeposit.mockResolvedValue({ success: false, message: 'server down' });

    const res = await ensureEscrowFunding(params({ amountBaseUnits: '100' }));

    expect(res).toEqual({ status: 'pending-confirm', txHash: '0xDEPOSIT_TX', deposited: '100' });
    // Exactly one stash, keyed by the deposit txHash, for this wallet/token/chain/amount.
    expect(mockAddPending).toHaveBeenCalledTimes(1);
    expect(mockAddPending).toHaveBeenCalledWith(
      expect.objectContaining({
        mode: 'deposit',
        token: TOKEN,
        chainId: CHAIN_ID,
        amount: '100',
        txHash: '0xDEPOSIT_TX',
        walletAddress: WALLET,
        createdAt: expect.any(Number),
      }),
    );
  });
});

describe('ensureEscrowFunding — balance read fallbacks', () => {
  it('treats a failed balance read as available=0 (full deposit required)', async () => {
    mockBalance.mockResolvedValue({ success: false });
    arrangeHappyChain();
    mockAllowance.mockResolvedValue({ success: true, data: { allowance: '1000' } });
    mockConfirmDeposit.mockResolvedValue({ success: true });

    const res = await ensureEscrowFunding(params({ amountBaseUnits: '100' }));

    expect(res).toEqual({ status: 'funded', deposited: '100' });
    expect(mockPrepareDeposit).toHaveBeenCalledWith(TOKEN, '100', WALLET, CHAIN_ID);
  });
});

describe('ensureEscrowFunding — prepare failures surface as throws (funds-safe contract)', () => {
  it('throws when prepareApprove fails (before any broadcast)', async () => {
    mockBalance.mockResolvedValue({ success: true, data: { available: '0', locked: '0' } });
    mockEnsureGas.mockResolvedValue(true);
    mockAllowance.mockResolvedValue({ success: true, data: { allowance: '0' } });
    mockPrepareApprove.mockResolvedValue({ success: false, message: 'Prepare approve failed' });

    await expect(ensureEscrowFunding(params())).rejects.toThrow(/Prepare approve failed/);
    expect(mockBroadcastTransaction).not.toHaveBeenCalled();
  });

  it('throws when prepareDeposit fails after a successful approve', async () => {
    mockBalance.mockResolvedValue({ success: true, data: { available: '0', locked: '0' } });
    arrangeHappyChain();
    mockAllowance.mockResolvedValue({ success: true, data: { allowance: '1000' } }); // skip approve
    mockPrepareDeposit.mockResolvedValue({ success: false, message: 'Prepare deposit failed' });

    await expect(ensureEscrowFunding(params())).rejects.toThrow(/Prepare deposit failed/);
    expect(mockConfirmDeposit).not.toHaveBeenCalled();
    expect(mockAddPending).not.toHaveBeenCalled();
  });
});

describe('ensureEscrowFunding — sign/broadcast failures are mapped like runP2PTx', () => {
  it('maps a raw approve signing error', async () => {
    mockBalance.mockResolvedValue({ success: true, data: { available: '0', locked: '0' } });
    arrangeHappyChain();
    mockAllowance.mockResolvedValue({ success: true, data: { allowance: '0' } });
    mockSignTransaction.mockRejectedValue(new Error('insufficient funds for gas * price + value: have 0 want 1000'));

    await expect(ensureEscrowFunding(params())).rejects.toThrow('Not enough native token to pay gas on this chain.');
    expect(mockBroadcastTransaction).not.toHaveBeenCalled();
  });

  it('maps a raw deposit broadcast error and never confirms or stashes', async () => {
    mockBalance.mockResolvedValue({ success: true, data: { available: '0', locked: '0' } });
    arrangeHappyChain();
    mockAllowance.mockResolvedValue({ success: true, data: { allowance: '1000' } });
    mockBroadcastTransaction.mockRejectedValue(new Error('nonce too low: next nonce 7, tx nonce 6'));

    await expect(ensureEscrowFunding(params())).rejects.toThrow(
      'Your wallet was out of step with the network. Please try again.',
    );
    expect(mockConfirmDeposit).not.toHaveBeenCalled();
    expect(mockAddPending).not.toHaveBeenCalled();
  });
});

// TSA Liquidity (review I4): an LP deposit must move EXACTLY the typed amount.
// With exactDepositBaseUnits set, a shortfall that differs (the balance moved
// since the caller read it, or this read failed) stops before any gas check,
// approve or broadcast.
describe('ensureEscrowFunding — exactDepositBaseUnits', () => {
  it('deposits the shortfall when it equals the exact amount', async () => {
    mockBalance.mockResolvedValue({ success: true, data: { available: '40', locked: '0' } });
    arrangeHappyChain();
    mockAllowance.mockResolvedValue({ success: true, data: { allowance: '1000' } });
    mockConfirmDeposit.mockResolvedValue({ success: true });

    const res = await ensureEscrowFunding(params({ amountBaseUnits: '100', exactDepositBaseUnits: '60' }));

    expect(res).toEqual({ status: 'funded', deposited: '60' });
    expect(mockPrepareDeposit).toHaveBeenCalledWith(TOKEN, '60', WALLET, CHAIN_ID);
  });

  it('aborts with no broadcast when the balance moved since the caller read it', async () => {
    mockBalance.mockResolvedValue({ success: true, data: { available: '30', locked: '0' } });
    arrangeHappyChain();

    const res = await ensureEscrowFunding(params({ amountBaseUnits: '100', exactDepositBaseUnits: '60' }));

    expect(res).toEqual({ status: 'aborted', reason: 'balance-changed' });
    expect(mockEnsureGas).not.toHaveBeenCalled();
    expect(mockPrepareApprove).not.toHaveBeenCalled();
    expect(mockBroadcastTransaction).not.toHaveBeenCalled();
  });

  it('aborts instead of assuming 0 when the balance read fails', async () => {
    mockBalance.mockResolvedValue({ success: false });
    arrangeHappyChain();

    const res = await ensureEscrowFunding(params({ amountBaseUnits: '100', exactDepositBaseUnits: '60' }));

    expect(res).toEqual({ status: 'aborted', reason: 'balance-changed' });
    expect(mockBroadcastTransaction).not.toHaveBeenCalled();
  });
});

describe('P2P gas sponsor (gasAction)', () => {
  it('asks the P2P sponsor when gasAction is set; LP (unset) keeps ensureGas', async () => {
    mockBalance.mockResolvedValue({ success: true, data: { available: '0', locked: '0' } });
    arrangeHappyChain();
    mockAllowance.mockResolvedValue({ success: true, data: { allowance: '1000' } });
    mockConfirmDeposit.mockResolvedValue({ success: true });
    mockPrepareDeposit.mockResolvedValue({ success: true, data: { unsignedTx: {} } });
    mockEnsureP2PGas.mockResolvedValue(true);

    await ensureEscrowFunding(params({ gasAction: 'deposit' }));
    expect(mockEnsureP2PGas).toHaveBeenCalledWith(expect.objectContaining({ chainId: CHAIN_ID, walletAddress: WALLET, action: 'deposit' }));
    expect(mockEnsureGas).not.toHaveBeenCalled();
  });

  it('requests one drip per step: approve before the approve, deposit before the deposit', async () => {
    const order: string[] = [];
    mockBalance.mockResolvedValue({ success: true, data: { available: '0', locked: '0' } });
    arrangeHappyChain();
    mockAllowance.mockResolvedValue({ success: true, data: { allowance: '0' } });
    mockConfirmDeposit.mockResolvedValue({ success: true });
    mockPrepareApprove.mockResolvedValue({ success: true, data: { unsignedTx: {} } });
    mockPrepareDeposit.mockResolvedValue({ success: true, data: { unsignedTx: {} } });
    mockWaitForTransaction.mockResolvedValue({ status: 1 });
    mockEnsureP2PGas.mockImplementation(async (a: any) => { order.push(`gas:${a.action}`); return true; });
    mockBroadcastTransaction.mockImplementation(async () => { order.push('broadcast'); return { hash: '0xh' }; });

    await ensureEscrowFunding(params({ gasAction: 'deposit' }));
    expect(order).toEqual(['gas:approve', 'broadcast', 'gas:deposit', 'broadcast']);
  });
});

describe('KYC refusals reach the caller with their code', () => {
  const kyc = { success: false, status: 403, code: 'kyc_verification_required', message: 'Identity verification required' };

  it('a KYC 403 on the balance read stops before any gas check', async () => {
    mockBalance.mockResolvedValue(kyc);
    await expect(ensureEscrowFunding(params())).rejects.toMatchObject({ code: 'kyc_verification_required', status: 403 });
    expect(mockEnsureGas).not.toHaveBeenCalled();
    expect(mockEnsureP2PGas).not.toHaveBeenCalled();
  });

  it('a KYC 403 on the allowance read is not read as a zero allowance', async () => {
    arrangeHappyChain();
    mockBalance.mockResolvedValue({ success: true, data: { available: '0' } });
    mockAllowance.mockResolvedValue(kyc);
    await expect(ensureEscrowFunding(params())).rejects.toMatchObject({ code: 'kyc_verification_required' });
    expect(mockPrepareApprove).not.toHaveBeenCalled();
  });

  it('keeps code and status when prepareApprove or prepareDeposit refuses', async () => {
    arrangeHappyChain();
    mockBalance.mockResolvedValue({ success: true, data: { available: '0' } });
    mockAllowance.mockResolvedValue({ success: true, data: { allowance: '0' } });
    mockPrepareApprove.mockResolvedValue(kyc);
    await expect(ensureEscrowFunding(params())).rejects.toMatchObject({ code: 'kyc_verification_required', status: 403 });

    mockAllowance.mockResolvedValue({ success: true, data: { allowance: '1000' } });
    mockPrepareDeposit.mockResolvedValue(kyc);
    await expect(ensureEscrowFunding(params())).rejects.toMatchObject({ code: 'kyc_verification_required', status: 403 });
  });

  it('a non-KYC balance failure still reads as zero', async () => {
    arrangeHappyChain();
    mockBalance.mockResolvedValue({ success: false, status: 500, message: 'boom' });
    mockAllowance.mockResolvedValue({ success: true, data: { allowance: '1000' } });
    mockConfirmDeposit.mockResolvedValue({ success: true });
    await expect(ensureEscrowFunding(params())).resolves.toMatchObject({ status: 'funded' });
  });
});
