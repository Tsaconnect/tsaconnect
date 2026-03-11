import { Stack } from "expo-router";

const CategoryLayout = () => {
  return (
    <Stack>
    <Stack.Screen
      name="add"
      options={{
        title: "Add Category",
      }}
    />
    <Stack.Screen
      name="edit"
      options={{
        title: "Edit Category",
      }}
    />
    </Stack>
  );
};

export default CategoryLayout;
