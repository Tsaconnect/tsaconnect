// A payment link as a picture: the QR on a branded card the merchant can share
// into WhatsApp (which can't take an image through its wa.me link) or save.
import { shortUrl } from './display';
import { PRINT_COLORS, SCAN_TO_PAY, testNotice } from './scanToPay';

export type ShareCard = {
  businessName: string;
  /** The big line: the amount for a one-time link, the label for a reusable one. */
  headline: string;
  /** Under it: what it's for, or the amounts a reusable link takes. */
  detail?: string;
  url: string;
  test?: boolean;
};

const W = 1080;
const H = 1350; // 4:5, what WhatsApp and most chats show uncropped
const PAD = 88;
const TEXT_W = W - PAD * 2;
const HEADER = 250;
const GAP = 40;
const QR_MAX = 600;
const GOLD = '#E5C04B'; // 9.5:1 on the ink header

/** Cuts text to fit maxWidth, ending "…". */
function ellipsize(text: string, measure: (s: string) => number, maxWidth: number): string {
  let cut = text.replace(/…$/, '');
  while (cut && measure(`${cut}…`) > maxWidth) cut = cut.slice(0, -1);
  return `${cut.trimEnd()}…`;
}

/**
 * Splits text into at most maxLines lines no wider than maxWidth. A word wider
 * than a whole line is cut with "…", and so is the last line when words are
 * left over. measure is the canvas's measureText.
 */
export function wrapLines(text: string, measure: (s: string) => number, maxWidth: number, maxLines: number): string[] {
  if (maxLines <= 0) return [];
  const words = text.trim().split(/\s+/).filter(Boolean);
  const lines: string[] = [];
  let line = '';
  let placed = 0;
  for (const word of words) {
    const next = line ? `${line} ${word}` : word;
    if (measure(next) <= maxWidth) {
      line = next;
      placed++;
      continue;
    }
    if (line) {
      lines.push(line);
      if (lines.length === maxLines) break;
    }
    line = measure(word) <= maxWidth ? word : ellipsize(word, measure, maxWidth);
    placed++;
  }
  if (line && lines.length < maxLines) lines.push(line);
  if (placed < words.length) lines[lines.length - 1] = ellipsize(lines[lines.length - 1], measure, maxWidth);
  return lines;
}

/** "Mama Ngozi Store" → "pay-mama-ngozi-store.png"; never anything but a-z, 0-9 and dashes. */
export function shareCardFileName(businessName: string, test?: boolean): string {
  const slug = businessName
    .normalize('NFKD').replace(/\p{M}/gu, '') // "Ọlá" → "Ola"
    .toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 40);
  return `pay-${slug || 'link'}${test ? '-test' : ''}.png`;
}

// The page only loads images from itself and data: URLs (public/_headers), so
// the QR already on screen is copied in as a data: URL, not a blob: one.
function loadImage(src: string): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const img = new Image();
    img.onload = () => resolve(img);
    img.onerror = () => reject(new Error('image failed to load'));
    img.src = src;
  });
}

// A copy at the size it is drawn, so the browser rasterises crisp modules
// instead of scaling up the small one on screen.
function svgDataUrl(svg: SVGSVGElement, size: number) {
  const copy = svg.cloneNode(true) as SVGSVGElement;
  copy.setAttribute('width', String(size));
  copy.setAttribute('height', String(size));
  return `data:image/svg+xml;charset=utf-8,${encodeURIComponent(new XMLSerializer().serializeToString(copy))}`;
}

/** The font stack a font utility resolves to on this page (tokens.css), read off a probe element. */
function fontStack(utility: 'font-sans' | 'font-mono', fallback: string): string {
  const probe = document.createElement('span');
  probe.className = utility;
  probe.hidden = true;
  document.body.append(probe);
  try {
    return getComputedStyle(probe).fontFamily || fallback;
  } finally {
    probe.remove();
  }
}

// A canvas draws in a fallback font until the web font has loaded, and it isn't
// redrawn after. A font that never comes (offline, blocked) is not worth waiting on.
async function fontsLoaded(uses: [font: string, text: string][]): Promise<void> {
  const loads = Promise.allSettled(uses.map(async ([font, text]) => document.fonts?.load(font, text)));
  await Promise.race([loads, new Promise((resolve) => setTimeout(resolve, 3000))]);
}

/** Draws the card around the QR rendered on screen and returns it as a PNG. */
export async function shareCardBlob(qr: SVGSVGElement, card: ShareCard): Promise<Blob> {
  const canvas = document.createElement('canvas');
  canvas.width = W;
  canvas.height = H;
  const ctx = canvas.getContext('2d');
  if (!ctx) throw new Error('canvas unavailable');

  const sans = fontStack('font-sans', 'sans-serif');
  const font = {
    brand: `600 30px ${sans}`,
    name: `700 52px ${sans}`,
    headline: `700 64px ${sans}`,
    detail: `400 36px ${sans}`,
    scan: `600 38px ${sans}`,
    url: `400 24px ${fontStack('font-mono', 'monospace')}`,
    test: `600 30px ${sans}`,
  };
  const link = shortUrl(card.url);
  const notice = testNotice('link');
  const [mark] = await Promise.all([
    loadImage('/tsa-mark.png').catch(() => null),
    fontsLoaded([
      [font.brand, 'TSA Connect'], [font.name, card.businessName], [font.headline, card.headline],
      [font.detail, card.detail ?? ''], [font.scan, SCAN_TO_PAY], [font.url, link], [font.test, notice],
    ]),
  ]);

  const lines = (text: string, f: string, max: number) => {
    ctx.font = f;
    return wrapLines(text, (s) => ctx.measureText(s).width, TEXT_W, max);
  };
  const write = (text: string, f: string, color: string, x: number, y: number) => {
    ctx.font = f;
    ctx.fillStyle = color;
    ctx.fillText(text, x, y);
  };

  ctx.fillStyle = '#FFFFFF';
  ctx.fillRect(0, 0, W, H);

  // Header band: who is being paid.
  ctx.fillStyle = PRINT_COLORS.ink;
  ctx.fillRect(0, 0, W, HEADER);
  let x = PAD;
  if (mark) {
    ctx.drawImage(mark, PAD, 58, 56, 56);
    x = PAD + 76;
  }
  ctx.textBaseline = 'middle';
  write('TSA Connect', font.brand, GOLD, x, 86);
  ctx.textBaseline = 'alphabetic';
  write(lines(card.businessName, font.name, 1)[0] ?? '', font.name, '#FFFFFF', PAD, 196);

  // What is asked for, down from the header: at most two lines of each.
  let y = HEADER + 90;
  for (const line of lines(card.headline, font.headline, 2)) {
    write(line, font.headline, PRINT_COLORS.ink, PAD, y);
    y += 74;
  }
  y -= 16;
  for (const line of lines(card.detail ?? '', font.detail, 2)) {
    write(line, font.detail, PRINT_COLORS.muted, PAD, y);
    y += 46;
  }
  const qrTop = y - 34 + GAP; // y is one line below the last text; back up to its descenders

  // The footer, up from the bottom edge: the test banner, the link, "Scan to pay".
  ctx.textAlign = 'center';
  let bottom = H - 64;
  if (card.test) {
    write(notice, font.test, PRINT_COLORS.test, W / 2, bottom);
    bottom -= 70;
  }
  write(lines(link, font.url, 1)[0] ?? '', font.url, PRINT_COLORS.muted, W / 2, bottom);
  bottom -= 54;
  write(SCAN_TO_PAY, font.scan, PRINT_COLORS.ink, W / 2, bottom);
  const qrBottom = bottom - 30 - GAP; // above the capitals of "Scan to pay"

  // The code in the space between, as big as fits.
  const size = Math.floor(Math.min(QR_MAX, qrBottom - qrTop));
  const qrImage = await loadImage(svgDataUrl(qr, size));
  ctx.drawImage(qrImage, (W - size) / 2, qrTop + Math.floor((qrBottom - qrTop - size) / 2), size, size);

  return new Promise((resolve, reject) =>
    canvas.toBlob((blob) => (blob ? resolve(blob) : reject(new Error('could not make the image'))), 'image/png'),
  );
}
