// What a contact-type provider charges on top of the platform's contact fee.
//
// Mirrors the API's contact branch of resolveServicePricing: an access fee is
// charged with the flat call-out beside it; without one, the listing price (or
// nothing) plus the per-area call-out tier. The flat call-out is only charged
// alongside an access fee. Same residential area is always free, so it never
// counts. When none is set the provider charges nothing extra, and the contact
// fee alone unlocks the service — there is nothing to check out. Tiers depend
// on the buyer's area, so `payable` means payable from SOME area.

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
  /** The flat call-out charged with the access fee, 0 when there is none. */
  calloutFeeUsd: number;
  /** Whether anything is payable through checkout after the contact fee. */
  payable: boolean;
}

const positive = (n?: number | null) => typeof n === 'number' && n > 0;

export function contactServiceCharge(
  listing: ContactListing | null | undefined,
  profile: ContactProfile | null | undefined,
): ContactServiceCharge {
  const accessFeeUsd = positive(profile?.accessFeeUsd) ? (profile!.accessFeeUsd as number) : 0;
  const calloutFeeUsd = accessFeeUsd > 0 && positive(profile?.calloutFeeUsd) ? (profile!.calloutFeeUsd as number) : 0;
  const payable =
    accessFeeUsd > 0 ||
    positive(listing?.price) ||
    positive(listing?.shippingSameCity) ||
    positive(listing?.shippingSameState) ||
    positive(listing?.shippingSameCountry) ||
    positive(listing?.shippingInternational);
  return { accessFeeUsd, calloutFeeUsd, payable };
}
