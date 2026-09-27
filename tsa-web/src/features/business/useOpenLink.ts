import { useQuery } from '@tanstack/react-query';
import { getOpenLink, type Mode } from './api';
import { queryKeys } from './queryKeys';

/** One reusable link and its latest payments, shared by its drawer and its poster. */
export const useOpenLink = (mode: Mode, id: string) =>
  useQuery({ queryKey: queryKeys.openLink(mode, id), queryFn: ({ signal }) => getOpenLink(mode, id, signal) });
