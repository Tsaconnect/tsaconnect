// utils/deviceLocale.ts
//
// Best-effort device region, used only as a fallback signal when defaulting
// which currency an unconfigured user probably wants (see
// hooks/useRampCurrency.ts) — never for anything authoritative. Reads Intl's
// resolved locale rather than a native module, so it costs no new dependency
// and degrades to null (never throws) on a runtime without full ICU.
export function deviceCountryCode(): string | null {
  try {
    const locale = Intl.DateTimeFormat().resolvedOptions().locale;
    // BCP-47 region subtag: "en-US" -> "US", "fr-CI" -> "CI". A plain regex
    // avoids depending on the newer Intl.Locale constructor for something
    // this approximate.
    const match = /-([A-Z]{2})(?:-|$)/.exec(locale);
    return match ? match[1] : null;
  } catch {
    return null;
  }
}
