// hooks/usePayAuthorization.ts
//
// The one payer authorization step for TSA Pay: `request` only opens the
// AuthorizationModal (PIN/biometric — see components/common/AuthorizationModal.tsx;
// do not add a second unlock path), and nothing runs until the payer actually
// authorizes. usePayTest, usePayNaira and usePayCrypto each hand it what to do
// once they have; the modal props they spread are this hook's `authorization`.
import { useCallback, useState } from 'react';

export type AuthorizationProps = {
  visible: boolean;
  title: string;
  /** What is being authorized, in words (the refund screen names amount, token, network and wallet). */
  description?: string;
  onAuthorized(): void;
  onCancel(): void;
};

export function usePayAuthorization<T>(opts: {
  title: (pending: T | null) => string;
  description?: (pending: T | null) => string | undefined;
  onAuthorized: (pending: T) => void;
}): { request(pending: T): void; authorization: AuthorizationProps } {
  const [visible, setVisible] = useState(false);
  const [pending, setPending] = useState<{ value: T } | null>(null);

  const request = useCallback((value: T) => {
    setPending({ value });
    setVisible(true);
  }, []);

  const onCancel = useCallback(() => {
    setVisible(false);
    setPending(null);
  }, []);

  const onAuthorized = () => {
    setVisible(false);
    if (!pending) return;
    const { value } = pending;
    setPending(null);
    opts.onAuthorized(value);
  };

  return {
    request,
    authorization: {
      visible,
      title: opts.title(pending?.value ?? null),
      description: opts.description?.(pending?.value ?? null),
      onAuthorized,
      onCancel,
    },
  };
}
