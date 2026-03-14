import React, { useContext } from 'react';
import { Stack } from "expo-router";
import { AppContext } from '../../AuthContext/AuthContext';
import { defaultScreenOptions } from '../../constants/navigation';

const Layout = () => {
  const { category } = useContext(AppContext);
  return (
    <Stack screenOptions={defaultScreenOptions}>
      <Stack.Screen
        name="categoryservice"
        options={{ title: `${category} Services` }}
      />
      <Stack.Screen
        name="servicedetail"
        options={{ title: `Service Details` }}
      />
    </Stack>
  );
};

export default Layout;
