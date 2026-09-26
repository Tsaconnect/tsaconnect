// Dev only: serve the web app on localhost against the PRODUCTION API
// (`TSA_PROD_API=yes npm run dev:prod-api`). The proxy presents the origin prod
// expects (WEB_APP_ORIGIN), so the cookie session works on localhost.
//
// Everything done here is real — a new key, a webhook or payout change hits the
// live merchant — and the prod session cookies sit on localhost, where any other
// local server can read them. So: an explicit opt-in, a warning at start, a red
// bar on every page, and sign out when you're done (that revokes the session).
// A stopgap until app.tsaconnectworld.com is live; delete it then.
import { mergeConfig } from 'vite';
import base from './vite.config.ts';

if (process.env.TSA_PROD_API !== 'yes') {
  throw new Error('dev:prod-api talks to the PRODUCTION API with real data. Run it as: TSA_PROD_API=yes npm run dev:prod-api');
}
console.warn('\n  ⚠  tsa-web → PRODUCTION API (https://tsa.mcgpchain.com). Real data; sign out when done.\n');

export default mergeConfig(base, {
  define: { 'import.meta.env.VITE_TSA_PROD_API': JSON.stringify('yes') },
  server: {
    port: 5174,
    strictPort: true,
    proxy: {
      '/api': {
        target: 'https://tsa.mcgpchain.com',
        changeOrigin: true,
        secure: true,
        headers: { origin: 'https://app.tsaconnectworld.com' },
      },
    },
  },
});
