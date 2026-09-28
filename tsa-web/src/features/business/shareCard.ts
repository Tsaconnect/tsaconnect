// A payment link as a picture: the QR on a branded card the merchant can share
// into WhatsApp (which can't take an image through its wa.me link) or save.
import { shortUrl } from './display';

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
const QR = 600;
const INK = '#16201F';
const MUTED = '#5B6664';
const GOLD = '#E5C04B';

/**
 * Splits text into at most maxLines lines no wider than maxWidth, the last one
 * ending "…" when the text doesn't fit. measure is the canvas's measureText.
 */
export function wrapLines(text: string, measure: (s: string) => number, maxWidth: number, maxLines: number): string[] {
  const words = text.trim().split(/\s+/).filter(Boolean);
  const lines: string[] = [];
  let line = '';
  let used = 0;
  for (const word of words) {
    const next = line ? `${line} ${word}` : word;
    if (!line || measure(next) <= maxWidth) {
      line = next;
      used++;
      continue;
    }
    lines.push(line);
    if (lines.length === maxLines) break;
    line = word;
    used++;
  }
  if (line && lines.length < maxLines) lines.push(line);
  const last = lines.length - 1;
  // Out of lines with words left over, or one word wider than the card: end on "…".
  if (last >= 0 && (used < words.length || measure(lines[last]) > maxWidth)) {
    let cut = lines[last];
    while (cut && measure(`${cut}…`) > maxWidth) cut = cut.slice(0, -1);
    lines[last] = `${cut.trimEnd()}…`;
  }
  return lines;
}

/** "Mama Ngozi Store" → "pay-mama-ngozi-store.png"; never anything but a-z, 0-9 and dashes. */
export function shareCardFileName(businessName: string): string {
  const slug = businessName.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 40);
  return `pay-${slug || 'link'}.png`;
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

const svgDataUrl = (svg: SVGSVGElement) =>
  `data:image/svg+xml;charset=utf-8,${encodeURIComponent(new XMLSerializer().serializeToString(svg))}`;

/** Draws the card around the QR rendered on screen and returns it as a PNG. */
export async function shareCardBlob(qr: SVGSVGElement, card: ShareCard): Promise<Blob> {
  const canvas = document.createElement('canvas');
  canvas.width = W;
  canvas.height = H;
  const ctx = canvas.getContext('2d');
  if (!ctx) throw new Error('canvas unavailable');
  const font = getComputedStyle(document.body).fontFamily || 'system-ui, sans-serif';
  const [qrImage, mark] = await Promise.all([loadImage(svgDataUrl(qr)), loadImage('/tsa-mark.png').catch(() => null)]);

  ctx.fillStyle = '#FFFFFF';
  ctx.fillRect(0, 0, W, H);

  // Header band: who is being paid.
  ctx.fillStyle = INK;
  ctx.fillRect(0, 0, W, 250);
  let x = PAD;
  if (mark) {
    ctx.drawImage(mark, PAD, 58, 56, 56);
    x = PAD + 76;
  }
  ctx.fillStyle = GOLD;
  ctx.font = `600 30px ${font}`;
  ctx.textBaseline = 'middle';
  ctx.fillText('TSA Connect', x, 86);
  ctx.fillStyle = '#FFFFFF';
  ctx.font = `700 52px ${font}`;
  ctx.textBaseline = 'alphabetic';
  ctx.fillText(wrapLines(card.businessName, (s) => ctx.measureText(s).width, W - PAD * 2, 1)[0] ?? '', PAD, 196);

  // What is asked for.
  let y = 340;
  ctx.fillStyle = INK;
  ctx.font = `700 64px ${font}`;
  for (const line of wrapLines(card.headline, (s) => ctx.measureText(s).width, W - PAD * 2, 2)) {
    ctx.fillText(line, PAD, y);
    y += 74;
  }
  if (card.detail) {
    ctx.fillStyle = MUTED;
    ctx.font = `400 36px ${font}`;
    for (const line of wrapLines(card.detail, (s) => ctx.measureText(s).width, W - PAD * 2, 2)) {
      ctx.fillText(line, PAD, y - 16);
      y += 46;
    }
  }

  // The code itself, centred in what is left above the footer.
  const qrTop = Math.max(y + 8, 520);
  ctx.drawImage(qrImage, (W - QR) / 2, qrTop, QR, QR);

  ctx.textAlign = 'center';
  ctx.fillStyle = INK;
  ctx.font = `600 38px ${font}`;
  ctx.fillText('Scan to pay with TSA Connect', W / 2, qrTop + QR + 72);
  ctx.fillStyle = MUTED;
  ctx.font = `400 24px ${getComputedStyle(document.documentElement).getPropertyValue('--font-mono') || 'monospace'}`;
  ctx.fillText(wrapLines(shortUrl(card.url), (s) => ctx.measureText(s).width, W - PAD * 2, 1)[0] ?? '', W / 2, qrTop + QR + 122);
  if (card.test) {
    ctx.fillStyle = '#9A6B00';
    ctx.font = `600 30px ${font}`;
    ctx.fillText('Test link — no real money moves', W / 2, H - 48);
  }

  return new Promise((resolve, reject) =>
    canvas.toBlob((blob) => (blob ? resolve(blob) : reject(new Error('could not make the image'))), 'image/png'),
  );
}
