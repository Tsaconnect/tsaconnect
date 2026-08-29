import { Stack } from "expo-router";
import { useDefaultScreenOptions } from "@/constants/navigation";

const CategoryLayout = () => {
  const screenOptions = useDefaultScreenOptions();
  return (
    <Stack screenOptions={screenOptions}>
      <Stack.Screen name="add" options={{ title: "Add Category" }} />
      <Stack.Screen name="edit" options={{ title: "Edit Category" }} />
    </Stack>
  );
};

export default CategoryLayout;
