import { Image, StyleSheet } from "react-native";
import { Tabs } from "expo-router";
import { COLORS } from "../../../constants";

const DashLayout = () => {
  return (
    <Tabs
      screenOptions={{
        tabBarActiveTintColor: COLORS.black,
        headerShown: false,
        tabBarLabelStyle: {
          fontSize: 11, // Increase the font size here
        },
      }}
    >
      <Tabs.Screen
        name="(home)"
        options={{
          title: "Tsa Connect",
          tabBarIcon: ({ color, focused }) => (
            <Image
              source={require("../../../assets/icons/tsa.png")}
              style={{
                height: 25,
                width: 25,
                tintColor: focused ? COLORS.primary : COLORS.black,
              }}
              resizeMode="contain"
            />
          ),
          headerTintColor: COLORS.primary,
          headerShown: false,
        }}
      />
      <Tabs.Screen
        name="(marketplace)"
        options={{
          title: "Marketplace",
          tabBarIcon: ({ color, focused }) => (
            /*   <TabBarIcon name={focused ? 'home' : 'home-outline'} color={color} /> */
            <Image
              source={require("../../../assets/icons/marketplace.png")}
              style={{
                height: 24,
                width: 24,
                tintColor: focused ? COLORS.primary : COLORS.black,
              }}
              resizeMode="contain"
            />
          ),
          headerShown: false,
        }}
      />
      <Tabs.Screen
        name="(services)"
        options={{
          title: "Services",
          tabBarIcon: ({ color, focused }) => (
            /*  <TabBarIcon name={focused ? 'person' : 'person'} color={color} /> */
            <Image
              source={require("../../../assets/icons/service.png")}
              style={{
                height: 24,
                width: 24,
                tintColor: focused ? COLORS.primary : COLORS.black,
              }}
              resizeMode="contain"
            />
          ),
          headerShown: false,
        }}
      />
      <Tabs.Screen
        name="easyswap"
        options={{
          title: "Easyswap",
          tabBarIcon: ({ color, focused }) => (
            /*  <TabBarIcon name={focused ? 'person' : 'person'} color={color} /> */
            <Image
              source={require("../../../assets/icons/swap.png")}
              style={{
                height: 24,
                width: 24,
                tintColor: focused ? COLORS.primary : COLORS.black,
              }}
              resizeMode="contain"
            />
          ),
          headerShown: false,
        }}
      />
      <Tabs.Screen
        name="tokenization"
        options={{
          title: "Wallet",
          tabBarIcon: ({ color, focused }) => (
            /*  <TabBarIcon name={focused ? 'person' : 'person'} color={color} /> */
            <Image
              source={require("../../../assets/appservices/wallet.png")}
              style={{
                height: 24,
                width: 24,
                tintColor: focused ? COLORS.primary : COLORS.black,
              }}
              resizeMode="contain"
            />
          ),
          headerShown: false,
        }}
      />
      <Tabs.Screen
        name="notifications"
        options={{
          title: 'Notifications',
          href: null,
        }}
      />
    </Tabs>
  );
};

export default DashLayout;

const styles = StyleSheet.create({});
