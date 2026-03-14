import {
  createMaterialTopTabNavigator,
} from "@react-navigation/material-top-tabs";
import { withLayoutContext } from "expo-router";

const { Navigator } = createMaterialTopTabNavigator();

export const MaterialTopTabs = withLayoutContext(Navigator);

import { Stack } from "expo-router";
import { useAuth } from "../../../../AuthContext/AuthContext";
import { defaultScreenOptions } from "../../../../constants/navigation";

export default function Layout() {
  const { appService } = useAuth();
  return (
    <Stack screenOptions={defaultScreenOptions}>
      <Stack.Screen name="serviceshome" options={{ headerShown: false }} />
      <Stack.Screen name="serviceaction" options={{ title: `${appService}` }} />
    </Stack>
  );
}
