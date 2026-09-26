// hooks/usePayTest.ts
//
// Approve/Decline for a test-mode payment on app/pay.tsx. `request` only
// opens the AuthorizationModal — nothing reaches the server until the payer
// actually authorizes (PIN/biometric), matching every other money-moving
// action in the app (see components/common/AuthorizationModal.tsx; do not
// add a second unlock path). `submitTestOutcome` fires from `onAuthorized`.
//
// AuthorizationProps lives in usePayAuthorization (the one authorization step
// every live payment hook shares); re-exported so existing imports keep working.
import { useCallback, useState } from 'react';
import { PayError, submitTestOutcome, type PayCheckout } from '@/services/payApi';
import { usePayAuthorization, type AuthorizationProps } from '@/hooks/usePayAuthorization';

export type { AuthorizationProps };

export type UsePayTest = {
  request(outcome: 'approve' | 'decline'): void;
  authorization: AuthorizationProps;
  submitting: boolean;
  error?: string;
  result?: PayCheckout;
};

export function usePayTest(paymentId: string): UsePayTest {
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | undefined>(undefined);
  const [result, setResult] = useState<PayCheckout | undefined>(undefined);

  const { request: open, authorization } = usePayAuthorization<'approve' | 'decline'>({
    title: (outcome) => (outcome === 'decline' ? 'Authorize decline' : 'Authorize payment'),
    onAuthorized: (outcome) => {
      setSubmitting(true);
      setError(undefined);
      submitTestOutcome(paymentId, outcome)
        .then((updated) => setResult(updated))
        .catch((err: unknown) => {
          setError(err instanceof PayError ? err.message : 'Something went wrong. Please try again.');
        })
        .finally(() => setSubmitting(false));
    },
  });

  const request = useCallback(
    (outcome: 'approve' | 'decline') => {
      setError(undefined);
      open(outcome);
    },
    [open],
  );

  return { request, authorization, submitting, error, result };
}
