// `servicedetail` is linked from seven screens outside this group (seller
// pages, subcategory grids, contact requests, the merchant dashboard), so it is
// route 0 of a fresh stack nearly every time it opens. The shared headerLeft in
// useDefaultScreenOptions() is what gives it a back chevron.
import React, { useContext } from "react";
import { Stack } from "expo-router";
import { AppContext } from "../../AuthContext/AuthContext";
import { useDefaultScreenOptions } from "@/constants/navigation";
import { MARKETPLACE_TAB } from "@/constants/routes";

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
  const screenOptions = useDefaultScreenOptions({ backFallback: MARKETPLACE_TAB });
  return (
    <Stack screenOptions={screenOptions}>
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
