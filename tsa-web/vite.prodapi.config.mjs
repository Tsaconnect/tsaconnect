// Dev only: serve the web app on localhost against the PRODUCTION API
// (`npm run dev:prod-api`). The proxy presents the origin prod expects
// (WEB_APP_ORIGIN), so the cookie session works on localhost. Everything you do
// here is real — keys, webhooks and payouts change the live merchant.
import { mergeConfig } from 'vite';
import base from './vite.config.ts';

export default mergeConfig(base, {
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
