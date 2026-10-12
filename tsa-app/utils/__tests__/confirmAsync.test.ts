const mockAlert = jest.fn();
jest.mock('react-native', () => ({ Alert: { alert: (...a: any[]) => mockAlert(...a) } }));
// eslint-disable-next-line import/first -- jest.mock factories hoist above this import.
import { confirmAsync } from '../confirmAsync';

beforeEach(() => mockAlert.mockReset());

it('resolves true on confirm and false on cancel', async () => {
  const yes = confirmAsync('T', 'M', 'Yes');
  mockAlert.mock.calls[0][2][1].onPress();
  await expect(yes).resolves.toBe(true);

  const no = confirmAsync('T', 'M', 'Yes', 'Not yet');
  expect(mockAlert.mock.calls[1][2][0]).toMatchObject({ text: 'Not yet', style: 'cancel' });
  mockAlert.mock.calls[1][2][0].onPress();
  await expect(no).resolves.toBe(false);
});

it('a dismissed dialog counts as cancel; destructive styles the confirm', async () => {
  const p = confirmAsync('T', 'M', 'Release', 'Not yet', true);
  expect(mockAlert.mock.calls[0][2][1].style).toBe('destructive');
  mockAlert.mock.calls[0][3].onDismiss();
  await expect(p).resolves.toBe(false);
});
