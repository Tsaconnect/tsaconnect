// utils/__tests__/p2pFollowUp.test.ts — applyFollowUp, the one dispatcher the
// trade, ad, My Ads and dispute screens hand every P2P outcome to.
import { Alert } from 'react-native';
import { applyFollowUp } from '../p2pFollowUp';
import { promptKycRefusal } from '../kycRefusal';
import { disputeFollowUp } from '../p2pTradeView';
import { adCancelFollowUp } from '../p2pAdCancel';
import { openFollowUp } from '../p2pAdOpen';

jest.mock('../kycRefusal', () => ({ ...jest.requireActual('../kycRefusal'), promptKycRefusal: jest.fn(() => true) }));
jest.mock('@react-native-async-storage/async-storage', () => ({ getItem: jest.fn().mockResolvedValue(null) }));

const alertSpy = jest.spyOn(Alert, 'alert').mockImplementation(() => {});
const handlers = () => ({ toast: { show: jest.fn() }, reload: jest.fn(), navigate: jest.fn() });

beforeEach(() => jest.clearAllMocks());

it('a KYC refusal routes to the Verify prompt and nothing else', () => {
  const h = handlers();
  applyFollowUp({ kyc: { code: 'kyc_verification_required', message: 'v' }, alert: { title: 't', message: 'm' }, reload: true }, h);
  expect(promptKycRefusal).toHaveBeenCalledWith('kyc_verification_required', 'v', 'p2p');
  expect(alertSpy).not.toHaveBeenCalled();
  expect(h.reload).not.toHaveBeenCalled();
});

it('toast, alert and reload', () => {
  const h = handlers();
  applyFollowUp({ toast: { type: 'success', message: 'Ad cancelled' }, alert: { title: 'T', message: 'M' }, reload: true }, h);
  expect(h.toast.show).toHaveBeenCalledWith('Ad cancelled', 'success');
  expect(alertSpy).toHaveBeenCalledWith('T', 'M', undefined);
  expect(h.reload).toHaveBeenCalled();
});

it('navigate without an alert goes straight there', () => {
  const h = handlers();
  applyFollowUp({ toast: { type: 'info', message: 'n' }, navigate: 't1', reload: false }, h);
  expect(h.navigate).toHaveBeenCalledWith('t1');
  expect(h.reload).not.toHaveBeenCalled();
});

it('navigate with an alert waits for its OK', () => {
  const h = handlers();
  applyFollowUp({ alert: { title: 'T', message: 'M' }, navigate: 't1', reload: false }, h);
  expect(h.navigate).not.toHaveBeenCalled();
  const buttons = alertSpy.mock.calls[0][2]!;
  expect(buttons.map((b) => b.text)).toEqual(['OK']);
  buttons[0].onPress?.();
  expect(h.navigate).toHaveBeenCalledWith('t1');
});

it('viewTradeId offers "View trade" next to the dismiss button', () => {
  const h = handlers();
  applyFollowUp({ alert: { title: 'T', message: 'M', viewTradeId: 't9', dismissText: 'Close' }, reload: false }, h);
  const buttons = alertSpy.mock.calls[0][2]!;
  expect(buttons.map((b) => b.text)).toEqual(['Close', 'View trade']);
  buttons[1].onPress?.();
  expect(h.navigate).toHaveBeenCalledWith('t9');
});

// Builder → dispatcher, end to end, per screen.
describe('screen follow-ups through applyFollowUp', () => {
  const HASH = '0x' + 'd'.repeat(64);

  it('dispute refused: a toast and no alert', () => {
    const h = handlers();
    applyFollowUp(disputeFollowUp({ status: 'refused', response: { success: false, message: 'p2p: invalid state transition' } }, 't1'), h);
    expect(h.toast.show).toHaveBeenCalledWith(expect.stringMatching(/already moved on/), 'error');
    expect(alertSpy).not.toHaveBeenCalled();
    expect(h.navigate).not.toHaveBeenCalled();
  });

  it('dispute confirmed: toast, then straight back to the trade', () => {
    const h = handlers();
    applyFollowUp(disputeFollowUp({ status: 'confirmed', txHash: HASH, prepared: {} }, 't1'), h);
    expect(h.toast.show).toHaveBeenCalledWith('Dispute opened. An admin will review it shortly.', 'success');
    expect(h.navigate).toHaveBeenCalledWith('t1');
    expect(alertSpy).not.toHaveBeenCalled();
  });

  it('dispute confirm-failed: back to the trade only after OK', () => {
    const h = handlers();
    applyFollowUp(disputeFollowUp({ status: 'confirm-failed', txHash: HASH, prepared: {}, message: 'm', mined: true }, 't1'), h);
    expect(h.navigate).not.toHaveBeenCalled();
    alertSpy.mock.calls[0][2]![0].onPress?.();
    expect(h.navigate).toHaveBeenCalledWith('t1');
  });

  it('My Ads cancel sent but not recorded: the alert, no toast, no reload', () => {
    const h = handlers();
    applyFollowUp(adCancelFollowUp({ status: 'confirm-failed', txHash: HASH, prepared: {}, message: 'm', mined: false }), h);
    expect(alertSpy.mock.calls[0][0]).toBe('Cancel sent — still confirming');
    expect(h.toast.show).not.toHaveBeenCalled();
    expect(h.reload).not.toHaveBeenCalled();
  });

  it('open confirm-failed: an info toast and straight to the trade, no alert', async () => {
    const h = handlers();
    const prepared = { trade: { id: 't7' } as never, unsignedTx: null };
    applyFollowUp(await openFollowUp({ status: 'confirm-failed', txHash: HASH, prepared, message: 'm', mined: false }, { id: 'ad1', side: 'sell' }), h);
    expect(h.toast.show).toHaveBeenCalledWith('Trade opened — still confirming the escrow.', 'info');
    expect(h.navigate).toHaveBeenCalledWith('t7');
    expect(alertSpy).not.toHaveBeenCalled();
  });
});
