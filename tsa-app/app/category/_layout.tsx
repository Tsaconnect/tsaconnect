import { Stack } from "expo-router";
import { defaultScreenOptions } from "../../constants/navigation";

const CategoryLayout = () => {
  return (
    <Stack screenOptions={defaultScreenOptions}>
      <Stack.Screen name="add" options={{ title: "Add Category" }} />
      <Stack.Screen name="edit" options={{ title: "Edit Category" }} />
    </Stack>
  );
};

export default CategoryLayout;
