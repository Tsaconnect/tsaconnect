import { Stack } from "expo-router";
import { defaultScreenOptions } from "../../constants/navigation";

const ProfileLayout = () => {
  return (
    <Stack screenOptions={defaultScreenOptions}>
      <Stack.Screen name="index" options={{ headerShown: false }} />
      <Stack.Screen name="edit" options={{ title: "Edit Profile" }} />
      <Stack.Screen name="edit-advert" options={{ title: "Edit Advert" }} />
      <Stack.Screen name="advert-details" options={{ title: "Advert Details" }} />
      <Stack.Screen name="verify-email" options={{ title: "Verify Email" }} />

    </Stack>
  );
};

export default ProfileLayout;
