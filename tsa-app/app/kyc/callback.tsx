// app/kyc/callback.tsx
//
// Landing route for the `tsaconnect://kyc/callback` deep link Didit redirects to
// when the hosted flow finishes outside the in-app WebView — e.g. the
// resubmission link Didit emails, opened in the system browser. Expo Router
// needs a real route at this path or the deep link falls through to +not-found.
//
// The redirect params (verificationSessionId, status) are deliberately ignored:
// the KYC screen re-reads status from our API on focus, and the webhook is the
// only trustworthy source of a verification outcome.
import { Redirect } from "expo-router";

export default function KYCCallback() {
  return <Redirect href="/profile/kyc" />;
}
