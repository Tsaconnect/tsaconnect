import { useQuery } from '@tanstack/react-query';
import { getOpenLink, type Mode } from './api';

export const openLinkKey = (mode: Mode, id: string) => ['open-link', mode, id] as const;

/** One reusable link and its latest payments, shared by its drawer and its poster. */
export const useOpenLink = (mode: Mode, id: string) =>
  useQuery({ queryKey: openLinkKey(mode, id), queryFn: ({ signal }) => getOpenLink(mode, id, signal) });
