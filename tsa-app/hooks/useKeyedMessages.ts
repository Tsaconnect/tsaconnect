// One message per list item (a request, an ad), so a failure stays on the row it
// belongs to while other rows act. set(id) with no message clears that row.
import { useCallback, useState } from 'react';

export function useKeyedMessages(): [Record<string, string>, (id: string, message?: string) => void] {
  const [messages, setMessages] = useState<Record<string, string>>({});
  const set = useCallback((id: string, message?: string) => {
    setMessages((prev) => {
      if (message) return { ...prev, [id]: message };
      if (!(id in prev)) return prev;
      const { [id]: _cleared, ...rest } = prev;
      return rest;
    });
  }, []);
  return [messages, set];
}
