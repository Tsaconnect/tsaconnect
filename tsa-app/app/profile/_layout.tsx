import { Stack } from "expo-router";
import { useDefaultScreenOptions } from "../../constants/navigation";

const ProfileLayout = () => {
  const screenOptions = useDefaultScreenOptions();
  return (
    <Stack screenOptions={screenOptions}>
      <Stack.Screen name="index" options={{ headerShown: false }} />
      <Stack.Screen name="edit" options={{ title: "Edit Profile" }} />
      <Stack.Screen name="edit-advert" options={{ title: "Edit Advert" }} />
      <Stack.Screen name="advert-details" options={{ title: "Advert Details" }} />
      <Stack.Screen name="verify-email" options={{ title: "Verify Email" }} />
      <Stack.Screen name="kyc" options={{ headerShown: false }} />
    </Stack>
  );
};

export default ProfileLayout;
