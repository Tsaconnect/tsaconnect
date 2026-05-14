import { useLocalSearchParams } from 'expo-router';
import ActivityScreen from '@/screens/activity';
import { type ActivityType } from '@/services/activityService';

export default function HistoryScreen() {
  const { types } = useLocalSearchParams<{ types?: string }>();
  const initialTypes = types ? (types.split(',') as ActivityType[]) : [];
  return <ActivityScreen initialTypes={initialTypes} />;
}
