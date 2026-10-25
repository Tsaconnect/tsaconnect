import { checkBackupAnswers, linkedWalletNotOnDevice, parseSetupMode } from '../walletSetup';

const words = ['a1', 'b2', 'c3', 'd4', 'e5', 'f6', 'g7', 'h8', 'i9', 'j10', 'k11', 'l12'];

describe('parseSetupMode', () => {
  it('knows the two paths and nothing else', () => {
    expect(parseSetupMode('create')).toBe('create');
    expect(parseSetupMode('import')).toBe('import');
    expect(parseSetupMode(['create', 'import'])).toBe('create');
    expect(parseSetupMode('restore')).toBeUndefined();
    expect(parseSetupMode(undefined)).toBeUndefined();
  });
});

describe('linkedWalletNotOnDevice', () => {
  it('is true only when the account has a wallet this phone does not hold', () => {
    expect(linkedWalletNotOnDevice('0xAbC', [])).toBe(true);
    expect(linkedWalletNotOnDevice('0xAbC', ['0xdef'])).toBe(true);
    expect(linkedWalletNotOnDevice('0xAbC', ['0xabc'])).toBe(false); // addresses compare without case
  });

  it('is false with nothing linked', () => {
    expect(linkedWalletNotOnDevice(undefined, [])).toBe(false);
    expect(linkedWalletNotOnDevice(null, ['0xabc'])).toBe(false);
    expect(linkedWalletNotOnDevice('', [])).toBe(false);
  });
});

describe('checkBackupAnswers', () => {
  const asked = [1, 4, 7, 10];

  it('confirms only when every asked word is right', () => {
    expect(checkBackupAnswers(words, asked, { 1: 'b2', 4: 'e5', 7: 'h8', 10: 'k11' })).toBe('correct');
  });

  it('reports a missing answer and a wrong one', () => {
    expect(checkBackupAnswers(words, asked, { 1: 'b2', 4: 'e5', 7: 'h8' })).toBe('incomplete');
    expect(checkBackupAnswers(words, asked, { 1: 'b2', 4: 'e5', 7: 'h8', 10: 'zzz' })).toBe('wrong');
  });

  it('proves nothing without a phrase on screen or without questions', () => {
    // These two used to be treated as a passed check.
    expect(checkBackupAnswers([], [], {})).toBe('unprepared');
    expect(checkBackupAnswers(words, [], {})).toBe('unprepared');
    expect(checkBackupAnswers(words.slice(0, 11), asked, { 1: 'b2', 4: 'e5', 7: 'h8', 10: 'k11' })).toBe('unprepared');
  });
});
