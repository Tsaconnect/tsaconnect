import * as Device from 'expo-device';
import Constants, { ExecutionEnvironment } from 'expo-constants';

// Whether push + reliable scheduled local notifications are available in this
// runtime. Requires a PHYSICAL device on a dev/standalone build. Expo Go
// (SDK 53+) removed Android remote push, so calling expo-notifications' push
// APIs there throws a console error — `Device.isDevice` is true in Expo Go (it
// IS a real phone), so we must additionally detect the Expo Go client and skip.
export const pushSupported: boolean =
  Device.isDevice && Constants.executionEnvironment !== ExecutionEnvironment.StoreClient;
