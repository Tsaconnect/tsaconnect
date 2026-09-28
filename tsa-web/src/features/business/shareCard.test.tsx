import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Drawer } from '@/components/ui/drawer';
import { toast } from '@/components/ui/toaster';
import { LinkShare } from './LinkShare';
import { shareCardBlob, shareCardFileName, wrapLines } from './shareCard';

vi.mock('./shareCard', async (actual) => ({
  ...(await actual<typeof import('./shareCard')>()),
  // jsdom has no canvas: the drawing is stood in for, everything around it is real.
  shareCardBlob: vi.fn(),
}));

// One character is 10 units wide.
const measure = (s: string) => s.length * 10;

describe('wrapLines', () => {
  it('fills lines word by word', () => {
    expect(wrapLines('Mama Ngozi Store Shop 12', measure, 110, 3)).toEqual(['Mama Ngozi', 'Store Shop', '12']);
  });

  it('ends on an ellipsis when the words run out of lines', () => {
    expect(wrapLines('Mama Ngozi Store Shop 12', measure, 110, 2)).toEqual(['Mama Ngozi', 'Store Shop…']);
  });

  it('cuts a single word wider than the card', () => {
    expect(wrapLines('Supercalifragilistic', measure, 60, 1)).toEqual(['Super…']);
  });

  it('cuts a word wider than the card on any line, never letting a line run over', () => {
    const first = wrapLines('Supercalifragilistic is long', measure, 60, 2);
    expect(first).toEqual(['Super…', 'is…']);
    const later = wrapLines('Pay Supercalifragilistic now', measure, 60, 3);
    expect(later).toEqual(['Pay', 'Super…', 'now']);
    for (const line of [...first, ...later]) expect(measure(line)).toBeLessThanOrEqual(60);
  });

  it('keeps text that fits as it is, to the last unit', () => {
    expect(wrapLines('  Sky   Air  ', measure, 200, 1)).toEqual(['Sky Air']);
    expect(wrapLines('Sky Air', measure, 70, 1)).toEqual(['Sky Air']);
  });

  it('has no lines for no text, or no room', () => {
    expect(wrapLines('', measure, 100, 2)).toEqual([]);
    expect(wrapLines('   ', measure, 100, 2)).toEqual([]);
    expect(wrapLines('Sky Air', measure, 100, 0)).toEqual([]);
  });
});

describe('shareCardFileName', () => {
  it('is safe for any business name', () => {
    expect(shareCardFileName('Mama Ngozi Store')).toBe('pay-mama-ngozi-store.png');
    expect(shareCardFileName('../../etc/passwd')).toBe('pay-etc-passwd.png');
    expect(shareCardFileName('☺')).toBe('pay-link.png');
  });

  it('keeps accented letters as their plain ones, and marks a test link', () => {
    expect(shareCardFileName('Café Ọlá')).toBe('pay-cafe-ola.png');
    expect(shareCardFileName('Sky Air', true)).toBe('pay-sky-air-test.png');
  });
});

const props = {
  url: 'https://tsaconnectworld.com/pay/abc',
  message: 'Sky Air is requesting ₦5,050.51 for Seat upgrade. Pay here: https://tsaconnectworld.com/pay/abc',
  card: { businessName: 'Sky Air', headline: '₦5,050.51', detail: 'Seat upgrade' },
};

const png = () => new Blob(['png'], { type: 'image/png' });

function shareable(share = vi.fn<(data: ShareData) => Promise<void>>(async () => {})) {
  vi.stubGlobal('navigator', { ...navigator, share, canShare: () => true });
  return share;
}

function noImageShare() {
  vi.stubGlobal('navigator', { ...navigator, share: undefined, canShare: undefined });
}

// jsdom has no object URLs.
function stubObjectUrls() {
  const create = vi.fn(() => 'blob:qr');
  const revoke = vi.fn();
  vi.stubGlobal('URL', class extends URL {
    static createObjectURL = create;
    static revokeObjectURL = revoke;
  });
  return { create, revoke };
}

const shareButton = () => screen.getByRole('button', { name: 'Share with QR code' });
const downloadButton = () => screen.getByRole('button', { name: 'Download QR' });

describe('LinkShare', () => {
  let toastError: ReturnType<typeof vi.spyOn>;
  beforeEach(() => {
    vi.mocked(shareCardBlob).mockReset().mockImplementation(async () => png());
    toastError = vi.spyOn(toast, 'error').mockImplementation(() => 1);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it('shares the QR card and the message together where the device can', async () => {
    const share = shareable();
    render(<LinkShare {...props} />);
    await userEvent.click(shareButton());
    expect(share).toHaveBeenCalledTimes(1);
    const [{ files, text }] = share.mock.calls[0] as unknown as [{ files: File[]; text: string }];
    expect(text).toBe(props.message);
    expect(files[0].name).toBe('pay-sky-air.png');
    expect(files[0].type).toBe('image/png');
  });

  it('draws the card on opening and shares inside the tap, as Safari needs', async () => {
    const share = shareable();
    render(<LinkShare {...props} />);
    await waitFor(() => expect(shareCardBlob).toHaveBeenCalledTimes(1));
    await act(() => new Promise((resolve) => setTimeout(resolve, 0))); // the drawing settles
    fireEvent.click(shareButton());
    // No await between the tap and this line: the share happened within the tap.
    expect(share).toHaveBeenCalledTimes(1);
    expect(shareCardBlob).toHaveBeenCalledTimes(1); // the drawing from opening, reused
    await waitFor(() => expect(shareButton()).not.toHaveAttribute('aria-busy'));
  });

  it('waits for a card not drawn yet, then shares it', async () => {
    let finish: (blob: Blob) => void = () => {};
    vi.mocked(shareCardBlob).mockImplementation(() => new Promise((resolve) => { finish = resolve; }));
    const share = shareable();
    render(<LinkShare {...props} />);
    fireEvent.click(shareButton());
    expect(share).not.toHaveBeenCalled();
    expect(shareButton()).toHaveAttribute('aria-busy', 'true');
    await act(async () => finish(png()));
    await waitFor(() => expect(share).toHaveBeenCalledTimes(1));
    expect(shareCardBlob).toHaveBeenCalledTimes(1);
  });

  it('treats closing the share sheet as a choice, not an error', async () => {
    const share = shareable(vi.fn(async () => { throw new DOMException('closed', 'AbortError'); }));
    render(<LinkShare {...props} />);
    await userEvent.click(shareButton());
    expect(share).toHaveBeenCalled();
    await waitFor(() => expect(shareButton()).not.toHaveAttribute('aria-busy'));
    expect(toastError).not.toHaveBeenCalled();
  });

  it('says so when the device refuses the share', async () => {
    shareable(vi.fn(async () => { throw new DOMException('not now', 'NotAllowedError'); }));
    render(<LinkShare {...props} />);
    await userEvent.click(shareButton());
    await waitFor(() => expect(toastError).toHaveBeenCalledWith("We couldn't share the QR code. Copy the link instead."));
  });

  it('says so when the card cannot be drawn, and tries again on the next tap', async () => {
    vi.mocked(shareCardBlob).mockRejectedValue(new Error('no canvas'));
    const share = shareable();
    render(<LinkShare {...props} />);
    await userEvent.click(shareButton());
    await waitFor(() => expect(toastError).toHaveBeenCalledWith("We couldn't share the QR code. Copy the link instead."));
    expect(share).not.toHaveBeenCalled();

    vi.mocked(shareCardBlob).mockImplementation(async () => png());
    await userEvent.click(shareButton());
    await waitFor(() => expect(share).toHaveBeenCalledTimes(1));
    expect(toastError).toHaveBeenCalledTimes(1);
  });

  it('offers only the text share and a download where images cannot be shared', () => {
    noImageShare();
    render(<LinkShare {...props} />);
    expect(screen.queryByRole('button', { name: 'Share with QR code' })).not.toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Share on WhatsApp' })).toHaveAttribute('href', expect.stringContaining('wa.me'));
    expect(downloadButton()).toBeInTheDocument();
    expect(shareCardBlob).not.toHaveBeenCalled(); // nothing drawn until it is wanted
  });

  it('saves the QR card as a PNG, named for the business and the mode', async () => {
    noImageShare();
    stubObjectUrls();
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
    render(<LinkShare {...props} card={{ ...props.card, test: true }} />);
    await userEvent.click(downloadButton());
    await waitFor(() => expect(click).toHaveBeenCalledTimes(1));
    const a = click.mock.instances[0] as unknown as HTMLAnchorElement;
    expect(a.download).toBe('pay-sky-air-test.png');
    expect(a.href).toBe('blob:qr');
  });

  it('says so when the QR card cannot be saved', async () => {
    noImageShare();
    vi.mocked(shareCardBlob).mockRejectedValue(new Error('no canvas'));
    render(<LinkShare {...props} />);
    await userEvent.click(downloadButton());
    await waitFor(() => expect(toastError).toHaveBeenCalledWith("We couldn't save the QR code. Try again."));
  });

  it("lets a saved file's address go a minute on, or when the link closes", async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    try {
      noImageShare();
      const { revoke } = stubObjectUrls();
      vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
      const { unmount } = render(<LinkShare {...props} />);

      fireEvent.click(downloadButton());
      await act(() => vi.advanceTimersByTimeAsync(59_000));
      expect(revoke).not.toHaveBeenCalled();
      await act(() => vi.advanceTimersByTimeAsync(1_000));
      expect(revoke).toHaveBeenCalledTimes(1);

      fireEvent.click(downloadButton());
      await act(() => vi.advanceTimersByTimeAsync(1_000));
      expect(revoke).toHaveBeenCalledTimes(1);
      unmount();
      expect(revoke).toHaveBeenCalledTimes(2);
    } finally {
      vi.useRealTimers();
    }
  });

  it('keeps the focus on the button while it works, inside a drawer', async () => {
    let finish: (blob: Blob) => void = () => {};
    vi.mocked(shareCardBlob).mockImplementation(() => new Promise((resolve) => { finish = resolve; }));
    shareable();
    render(<Drawer open onOpenChange={() => {}} title="Payment"><LinkShare {...props} /></Drawer>);
    await userEvent.click(shareButton());
    // Busy, not disabled: a browser drops the focus from a focused button that becomes disabled.
    expect(shareButton()).toHaveAttribute('aria-busy', 'true');
    expect(shareButton()).not.toBeDisabled();
    expect(shareButton()).toHaveFocus();
    expect(downloadButton()).not.toHaveAttribute('aria-busy');
    await act(async () => finish(png()));
    await waitFor(() => expect(shareButton()).not.toHaveAttribute('aria-busy'));
    expect(shareButton()).toHaveFocus();
  });
});
