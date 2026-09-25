/** Shown only by `npm run dev:prod-api`: this local build is using live production data. */
export function ProdApiBar() {
  if (import.meta.env.VITE_TSA_PROD_API !== 'yes') return null;
  return (
    <div role="status" className="sticky top-0 z-[60] bg-danger-strong px-4 py-1.5 text-center text-caption font-semibold text-white">
      Local build on LIVE production data — changes here are real. Sign out when you're done.
    </div>
  );
}
