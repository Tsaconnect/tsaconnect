// utils/__tests__/p2pFollowUp.test.ts — applyFollowUp, the one dispatcher the
// trade, ad, My Ads and dispute screens hand every P2P outcome to.
import { Alert } from 'react-native';
import { applyFollowUp } from '../p2pFollowUp';
import { promptKycRefusal } from '../kycRefusal';

jest.mock('../kycRefusal', () => ({ promptKycRefusal: jest.fn(() => true) }));

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
