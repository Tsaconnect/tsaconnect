import { disputeRefusalMessage } from '../p2pDispute';

describe('disputeRefusalMessage', () => {
  it('says a dispute is already open when the server says so', () => {
    expect(disputeRefusalMessage({ status: 409, message: 'p2p: trade already has an open dispute' })).toBe(
      'A dispute is already open for this trade.',
    );
  });

  it('explains an unrecognised 409 as another prepare in progress', () => {
    expect(disputeRefusalMessage({ status: 409, message: 'p2p: dispute is being prepared by another party' })).toMatch(
      /already being prepared.*few minutes/i,
    );
  });

  it('keeps coded 409s on their own copy', () => {
    expect(disputeRefusalMessage({ status: 409, code: 'sponsorship_in_progress', message: 'x' })).toBe(
      'A gas top-up is already in progress. Give it a moment.',
    );
  });

  it('leaves other statuses to the shared mapper', () => {
    expect(disputeRefusalMessage({ status: 401, message: 'nope' })).toBe('Your session has expired. Sign in again.');
  });
});
