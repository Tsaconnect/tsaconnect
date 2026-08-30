// __tests__/back.test.ts
//
// The back action's policy, which every back control in the app routes through
// (the native headers' headerLeft, ScreenHeader, and the P2P hardware-back
// handler). The two branches are the whole point: pop when there IS history,
// and land somewhere real when there is not.
import { goBack, hasHistory } from '@/utils/back';

// `mock`-prefixed so jest's out-of-scope guard allows them in the factory.
const mockBack = jest.fn();
const mockReplace = jest.fn();
const mockCanGoBack = jest.fn();

jest.mock('expo-router', () => ({
  router: {
    back: () => mockBack(),
    replace: (href: unknown) => mockReplace(href),
    canGoBack: () => mockCanGoBack(),
  },
}));

const back = mockBack;
const replace = mockReplace;
const canGoBack = mockCanGoBack;

beforeEach(() => {
  back.mockClear();
  replace.mockClear();
  canGoBack.mockReset();
});

describe('goBack', () => {
  it('pops one screen when there is history', () => {
    canGoBack.mockReturnValue(true);

    goBack('/home');

    expect(back).toHaveBeenCalledTimes(1);
    expect(replace).not.toHaveBeenCalled();
  });

  it('never navigates to the fallback while history exists', () => {
    // The "back takes me to main" reports came from controls that jumped to a
    // fixed route instead of popping. History always wins.
    canGoBack.mockReturnValue(true);

    goBack('/tokenization');

    expect(replace).not.toHaveBeenCalled();
  });

  it('replaces with the fallback when there is nothing to pop', () => {
    // Cold entry: a push-notification tap, a deep link, an OAuth callback. An
    // unguarded router.back() is a silent no-op here and traps the user.
    canGoBack.mockReturnValue(false);

    goBack('/tokenization');

    expect(back).not.toHaveBeenCalled();
    expect(replace).toHaveBeenCalledWith('/tokenization');
  });

  it('replaces rather than pushes, so the fallback becomes the new root', () => {
    canGoBack.mockReturnValue(false);

    goBack('/serviceshome');

    expect(replace).toHaveBeenCalledTimes(1);
  });
});

describe('hasHistory', () => {
  it('reports whether a back control has anywhere to go', () => {
    canGoBack.mockReturnValue(true);
    expect(hasHistory()).toBe(true);

    canGoBack.mockReturnValue(false);
    expect(hasHistory()).toBe(false);
  });
});
