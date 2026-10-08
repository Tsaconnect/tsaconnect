import { normalizePayCode, payCodeFieldText, payCodeLookupError, PAY_CODE_NOT_FOUND, PAY_CODE_TOO_MANY } from '../payCode';

describe('normalizePayCode (mirrors the API)', () => {
  it.each([
    ['TSA-7K2QF-M9XWD', '7K2QFM9XWD'],
    ['tsa-7k2qf-m9xwd', '7K2QFM9XWD'],
    ['7K2QF M9XWD', '7K2QFM9XWD'],
    [' TSA–7K2QF—M9XWD\n', '7K2QFM9XWD'], // pasted from a chat app
    ['TSA-7K2QF-M9XWO', '7K2QFM9XW0'],
    ['TSA-7K2QF-M9XIL', '7K2QFM9X11'],
    ['TSA12345XY', 'TSA12345XY'], // a bare code that itself starts with TSA
    ['7k2qfm9xwd', '7K2QFM9XWD'],
    ['TSA 7K2QF M9XWD\n', '7K2QFM9XWD'],
    ['TSA\u00A07K2QF\u2013M9XWD', '7K2QFM9XWD'], // NBSP, en dash
    ['TSA-TSA12-345XY', 'TSA12345XY'],
    ['TSA\u30007K2QF\u{10EAD}M9XWD', '7K2QFM9XWD'], // ideographic space, astral Yezidi hyphen: on the explicit lists
  ])('%j is %s', (input, want) => {
    expect(normalizePayCode(input)).toBe(want);
  });

  it.each(['', 'TSA', '7K2QF-M9XW', '7K2QF-M9XWDD', 'TSA-7K2QF-M9XWU', 'TSA-7K2QF-M9XWD-1',
  '7K2QF-M9XW!', 'A'.repeat(100),
  'TSA-7K2QF-M9XW\u0131', 'TSA-7K2QF-M9XW\u017F', 'TSA-7K2QF-M9XW\u00DF', '7K2QFM9X\u00DF', // ı ſ ß fold to ASCII; a code is ASCII
  '7K2QF\u{10D6E}M9XWD', '7K2QF\uFEFFM9XWD', // Garay hyphen (Unicode 16 Pd) is on no list; BOM is no space
])('%j is not a code', (input) => {
    expect(normalizePayCode(input)).toBeNull();
  });
});

describe('payCodeFieldText', () => {
  it('groups what she types into XXXXX-XXXXX, the TSA- being drawn beside the field', () => {
    expect(payCodeFieldText('7k2qf')).toBe('7K2QF');
    expect(payCodeFieldText('7k2qfm')).toBe('7K2QF-M');
    expect(payCodeFieldText('7K2QF-M9XWD')).toBe('7K2QF-M9XWD');
  });
  it('takes a whole pasted TSA-… code', () => {
    expect(payCodeFieldText('TSA-7K2QF-M9XWD')).toBe('7K2QF-M9XWD');
  });
  it('keeps a bare code that starts with TSA whole', () => {
    expect(payCodeFieldText('TSA12345XY')).toBe('TSA12-345XY');
  });
  it('stops at ten characters', () => {
    expect(payCodeFieldText('7K2QFM9XWDQQ')).toBe('7K2QF-M9XWD');
  });
});

describe('payCodeLookupError', () => {
  it('says a code was not found, or to wait, in her words', () => {
    expect(payCodeLookupError({ status: 404, message: 'x' })).toBe(PAY_CODE_NOT_FOUND);
    expect(payCodeLookupError({ status: 429, message: 'x' })).toBe(PAY_CODE_TOO_MANY);
    expect(payCodeLookupError({ status: 0, message: 'Could not reach the server.' })).toBe('Could not reach the server.');
  });
});
