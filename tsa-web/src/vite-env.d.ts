/// <reference types="vite/client" />

interface ImportMetaEnv {
  /** "yes" only under `npm run dev:prod-api` (vite.prodapi.config.mjs). */
  readonly VITE_TSA_PROD_API?: string;
}
