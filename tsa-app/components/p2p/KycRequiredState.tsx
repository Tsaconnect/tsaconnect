// The P2P "verify first" state on the market (KYC 403 on /p2p/ads). Branches
// on the machine code, never the message; the copy is kycRefusalFor's, the
// same the Verify prompt shows.
import React from 'react';
import { router } from 'expo-router';
import { EmptyState } from '@/components/ui';
import { KYC_ROUTE, KYC_VERIFICATION_REQUIRED, kycRefusalFor } from '@/utils/kycRefusal';

export const KycRequiredState: React.FC<{ code?: string }> = ({ code }) => {
  // An unrecognised code still means the P2P gate refused: ask to verify.
  const refusal = kycRefusalFor(code, undefined, 'p2p') ?? kycRefusalFor(KYC_VERIFICATION_REQUIRED, undefined, 'p2p')!;
  return refusal.kind === 'support' ? (
    <EmptyState icon="info" title={refusal.title} subtitle={refusal.message} />
  ) : (
    <EmptyState
      icon="lock"
      title={refusal.title}
      subtitle={refusal.message}
      actionLabel="Verify now"
      onAction={() => router.push(KYC_ROUTE as never)}
    />
  );
};
