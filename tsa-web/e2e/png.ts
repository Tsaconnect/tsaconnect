// Just enough PNG decoding to look at the pixels of a canvas's toBlob output:
// 8-bit RGB or RGBA, not interlaced. Node's zlib does the inflating.
import { inflateSync } from 'node:zlib';

export type Pixels = { width: number; height: number; at: (x: number, y: number) => [r: number, g: number, b: number] };

export function decodePng(file: Buffer): Pixels {
  if (file.subarray(1, 4).toString() !== 'PNG') throw new Error('not a PNG');
  let width = 0;
  let height = 0;
  let channels = 0;
  const data: Buffer[] = [];
  for (let at = 8; at < file.length;) {
    const length = file.readUInt32BE(at);
    const type = file.subarray(at + 4, at + 8).toString();
    const body = file.subarray(at + 8, at + 8 + length);
    if (type === 'IHDR') {
      width = body.readUInt32BE(0);
      height = body.readUInt32BE(4);
      const [depth, color, , , interlace] = body.subarray(8, 13);
      if (depth !== 8 || interlace !== 0 || (color !== 2 && color !== 6)) throw new Error(`unsupported PNG: depth ${depth}, colour ${color}`);
      channels = color === 6 ? 4 : 3;
    } else if (type === 'IDAT') data.push(body);
    at += 12 + length;
  }

  // Each row is a filter byte, then the row's bytes filtered against the left and upper pixels.
  const raw = inflateSync(Buffer.concat(data));
  const stride = width * channels;
  const out = Buffer.alloc(stride * height);
  for (let y = 0; y < height; y++) {
    const filter = raw[y * (stride + 1)];
    for (let i = 0; i < stride; i++) {
      const byte = raw[y * (stride + 1) + 1 + i];
      const left = i >= channels ? out[y * stride + i - channels] : 0;
      const up = y > 0 ? out[(y - 1) * stride + i] : 0;
      const upLeft = y > 0 && i >= channels ? out[(y - 1) * stride + i - channels] : 0;
      let predicted = 0;
      if (filter === 1) predicted = left;
      else if (filter === 2) predicted = up;
      else if (filter === 3) predicted = (left + up) >> 1;
      else if (filter === 4) {
        const p = left + up - upLeft;
        const [pa, pb, pc] = [Math.abs(p - left), Math.abs(p - up), Math.abs(p - upLeft)];
        predicted = pa <= pb && pa <= pc ? left : pb <= pc ? up : upLeft;
      }
      out[y * stride + i] = (byte + predicted) & 0xff;
    }
  }
  return {
    width,
    height,
    at: (x, y) => {
      const i = y * stride + x * channels;
      return [out[i], out[i + 1], out[i + 2]];
    },
  };
}

/** The fraction of the pixels in a box that pass a test. */
export function fraction(png: Pixels, box: { x: number; y: number; w: number; h: number }, test: (rgb: [number, number, number]) => boolean): number {
  let hits = 0;
  for (let y = box.y; y < box.y + box.h; y++) {
    for (let x = box.x; x < box.x + box.w; x++) if (test(png.at(x, y))) hits++;
  }
  return hits / (box.w * box.h);
}
