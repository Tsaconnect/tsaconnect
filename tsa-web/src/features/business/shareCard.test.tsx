import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { LinkShare } from './LinkShare';
import { shareCardFileName, wrapLines } from './shareCard';

vi.mock('./shareCard', async (actual) => ({
  ...(await actual<typeof import('./shareCard')>()),
  // jsdom has no canvas: the drawing is stood in for, everything around it is real.
  shareCardBlob: vi.fn(async () => new Blob(['png'], { type: 'image/png' })),
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

  it('keeps text that fits as it is', () => {
    expect(wrapLines('  Sky   Air  ', measure, 200, 1)).toEqual(['Sky Air']);
  });
});

describe('shareCardFileName', () => {
  it('is safe for any business name', () => {
    expect(shareCardFileName('Mama Ngozi Store')).toBe('pay-mama-ngozi-store.png');
    expect(shareCardFileName('../../etc/passwd')).toBe('pay-etc-passwd.png');
    expect(shareCardFileName('☺')).toBe('pay-link.png');
  });
});

const props = {
  url: 'https://tsaconnectworld.com/pay/abc',
  message: 'Sky Air is requesting ₦5,050.51 for Seat upgrade. Pay here: https://tsaconnectworld.com/pay/abc',
  card: { businessName: 'Sky Air', headline: '₦5,050.51', detail: 'Seat upgrade' },
};

describe('LinkShare', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('shares the QR card and the message together where the device can', async () => {
    const share = vi.fn(async () => {});
    vi.stubGlobal('navigator', { ...navigator, share, canShare: () => true });
    render(<LinkShare {...props} />);
    await userEvent.click(screen.getByRole('button', { name: 'Share with QR code' }));
    expect(share).toHaveBeenCalledTimes(1);
    const [{ files, text }] = share.mock.calls[0] as unknown as [{ files: File[]; text: string }];
    expect(text).toBe(props.message);
    expect(files[0].name).toBe('pay-sky-air.png');
    expect(files[0].type).toBe('image/png');
  });

  it('offers only the text share and a download where images cannot be shared', () => {
    vi.stubGlobal('navigator', { ...navigator, share: undefined, canShare: undefined });
    render(<LinkShare {...props} />);
    expect(screen.queryByRole('button', { name: 'Share with QR code' })).not.toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Share on WhatsApp' })).toHaveAttribute('href', expect.stringContaining('wa.me'));
    expect(screen.getByRole('button', { name: 'Download QR' })).toBeInTheDocument();
  });

  it('saves the QR card as a PNG', async () => {
    vi.stubGlobal('navigator', { ...navigator, share: undefined, canShare: undefined });
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
    URL.createObjectURL = vi.fn(() => 'blob:qr');
    URL.revokeObjectURL = vi.fn();
    render(<LinkShare {...props} />);
    await userEvent.click(screen.getByRole('button', { name: 'Download QR' }));
    expect(click).toHaveBeenCalledTimes(1);
    const a = click.mock.instances[0] as unknown as HTMLAnchorElement;
    expect(a.download).toBe('pay-sky-air.png');
    click.mockRestore();
  });

  it('treats closing the share sheet as a choice, not an error', async () => {
    const share = vi.fn(async () => { throw new DOMException('closed', 'AbortError'); });
    vi.stubGlobal('navigator', { ...navigator, share, canShare: () => true });
    render(<LinkShare {...props} />);
    await userEvent.click(screen.getByRole('button', { name: 'Share with QR code' }));
    expect(share).toHaveBeenCalled();
    expect(screen.getByRole('button', { name: 'Share with QR code' })).toBeEnabled();
  });
});
