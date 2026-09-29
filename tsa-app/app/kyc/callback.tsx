// app/kyc/callback.tsx
//
// Landing route for the `tsaconnect://kyc/callback` deep link Didit redirects to
// when the hosted flow finishes outside the in-app WebView — e.g. the
// resubmission link Didit emails, opened in the system browser. Expo Router
// needs a real route at this path or the deep link falls through to +not-found.
//
// A name change's session comes back with `?flow=name_change`: it belongs to
// the name-change screen, never to first-time KYC (which would only say "Identity
// Verified" — she already is). The other redirect params (verificationSessionId,
// status) are deliberately ignored: each screen re-reads status from our API on
// focus, and the webhook is the only trustworthy source of a verification outcome.
import { Redirect, useLocalSearchParams } from "expo-router";
import { NAME_CHANGE } from "@/constants/routes";

export default function KYCCallback() {
  const { flow } = useLocalSearchParams<{ flow?: string }>();
  return <Redirect href={flow === "name_change" ? NAME_CHANGE : "/profile/kyc"} />;
}
