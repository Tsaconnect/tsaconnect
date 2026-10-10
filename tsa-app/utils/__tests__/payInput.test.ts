/**
 * What the Pay screen opens from a pasted value: a code, or a one-time pay
 * link read down to its payment id — never anything else in the URL.
 */
import { parsePayInput } from '../payCode';

const ID = '3f2c1a9e-8b7d-4c6e-9f10-1a2b3c4d5e6f';

it('reads a code however it was written', () => {
  expect(parsePayInput(' tsa-7k2qf-m9xwd \n')).toEqual({ kind: 'code', code: '7K2QFM9XWD' });
});

it('reads a hosted pay link down to its id', () => {
  expect(parsePayInput(`https://tsaconnectworld.com/pay/${ID}`)).toEqual({ kind: 'payment', id: ID });
  expect(parsePayInput(`https://www.tsaconnectworld.com/pay/${ID.toUpperCase()}/`)).toEqual({ kind: 'payment', id: ID });
  expect(parsePayInput(`https://tsaconnectworld.com/pay/${ID}?utm_source=whatsapp`)).toEqual({ kind: 'payment', id: ID });
});

it("reads the app's own link", () => {
  expect(parsePayInput(`tsaconnect://pay?id=${ID}`)).toEqual({ kind: 'payment', id: ID });
  expect(parsePayInput(`tsaconnect://pay?ref=x&id=${ID}`)).toEqual({ kind: 'payment', id: ID });
});

it('refuses everything else', () => {
  for (const text of [
    'https://tsaconnectworld.com/pay/l/shop-12', // reusable link: the browser asks the amount
    `http://tsaconnectworld.com/pay/${ID}`, // not https
    `https://tsaconnectworld.com.evil.example/pay/${ID}`,
    `https://evil.example/pay/${ID}`,
    `https://tsaconnectworld.com/pay/${ID}/extra`,
    // Read as our host by React Native's regex URL, as evil.example by a browser.
    `https://evil.example#@tsaconnectworld.com/pay/${ID}`,
    `https://evil.example\\@tsaconnectworld.com/pay/${ID}`,
    `https://user@tsaconnectworld.com/pay/${ID}`,
    'https://tsaconnectworld.com/pay/not-a-uuid',
    'tsaconnect://pay?id=../../wallet',
    'hello',
    '',
  ]) {
    expect(parsePayInput(text)).toBeNull();
  }
});
