import { Stack } from "expo-router";
import { useDefaultScreenOptions } from "@/constants/navigation";

const ProfileLayout = () => {
  const screenOptions = useDefaultScreenOptions();
  return (
    <Stack screenOptions={screenOptions}>
      <Stack.Screen name="index" options={{ headerShown: false }} />
      <Stack.Screen name="edit" options={{ title: "Edit Profile" }} />
      <Stack.Screen name="advert-details" options={{ title: "Advert Details" }} />
      {/* <Verify> brings its own header row and heading. */}
      <Stack.Screen name="verify-email" options={{ headerShown: false }} />
      <Stack.Screen name="kyc" options={{ headerShown: false }} />
      <Stack.Screen name="name-change" options={{ headerShown: false }} />
    </Stack>
  );
};

export default ProfileLayout;
