// What a contact-type provider charges on top of the platform's contact fee.
//
// Mirrors the API's contact branch of resolveServicePricing: the access fee and
// call-out are flat; failing those the listing price; failing that the per-area
// call-out tiers (same residential area is always free, so it never counts).
// When none is set the provider charges nothing extra, and the contact fee alone
// unlocks the service — there is nothing to check out.

interface ContactListing {
  price?: number | null;
  shippingSameCity?: number | null;
  shippingSameState?: number | null;
  shippingSameCountry?: number | null;
  shippingInternational?: number | null;
}

interface ContactProfile {
  accessFeeUsd?: number | null;
  calloutFeeUsd?: number | null;
}

export interface ContactServiceCharge {
  /** The provider's own access fee in USD, 0 when they left it blank. */
  accessFeeUsd: number;
  /** Whether anything is payable through checkout after the contact fee. */
  payable: boolean;
}

const positive = (n?: number | null) => typeof n === 'number' && n > 0;

export function contactServiceCharge(
  listing: ContactListing | null | undefined,
  profile: ContactProfile | null | undefined,
): ContactServiceCharge {
  const accessFeeUsd = positive(profile?.accessFeeUsd) ? (profile!.accessFeeUsd as number) : 0;
  const payable =
    accessFeeUsd > 0 ||
    positive(profile?.calloutFeeUsd) ||
    positive(listing?.price) ||
    positive(listing?.shippingSameCity) ||
    positive(listing?.shippingSameState) ||
    positive(listing?.shippingSameCountry) ||
    positive(listing?.shippingInternational);
  return { accessFeeUsd, payable };
}
