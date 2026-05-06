import React, { useContext } from "react";
import { Stack } from "expo-router";
import { AppContext } from "../../AuthContext/AuthContext";
import { defaultScreenOptions } from "../../constants/navigation";

// Avoid double-suffixing titles like "Car rental services Services" when the
// category name already ends with "service"/"services".
function categoryTitle(category: string | undefined): string {
  if (!category) return "Services";
  const trimmed = category.trim();
  if (/services?$/i.test(trimmed)) return trimmed;
  return `${trimmed} Services`;
}

const Layout = () => {
  const { category } = useContext(AppContext);
  return (
    <Stack screenOptions={defaultScreenOptions}>
      <Stack.Screen
        name="categoryservice"
        options={{ title: categoryTitle(category) }}
      />
      <Stack.Screen
        name="servicedetail"
        options={{ title: "Service Details" }}
      />
    </Stack>
  );
};

export default Layout;
