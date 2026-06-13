// app/wallet/history.tsx
//
// Wallet transaction history — full activity feed pre-filtered by the optional
// `types` query param (e.g. opened from a specific feature). Thin route wrapper
// over <ActivityScreen> / <ActivityFeed>; carries no chrome of its own. The feed
// chain was restyled onto the TSA Signature design system (screens/activity.tsx
// + components/activity/ActivityFeed.tsx); the history fetch, pagination, filter
// param parsing, and per-item navigation are preserved verbatim here.
import { useLocalSearchParams } from 'expo-router';
import ActivityScreen from '@/screens/activity';
import { type ActivityType } from '@/services/activityService';

export default function HistoryScreen() {
  const { types } = useLocalSearchParams<{ types?: string }>();
  const initialTypes = types ? (types.split(',') as ActivityType[]) : [];
  return <ActivityScreen initialTypes={initialTypes} />;
}
