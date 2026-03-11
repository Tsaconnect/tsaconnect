import { Text, TouchableOpacity, View } from "react-native";
import React from "react";
import { router } from "expo-router";
import { COLORS } from "../../constants";
//@ts-ignore
const TabButton = ({ link, buttonTitle, active }) => {
  function handleNavigation() {
    //@ts-ignore
    router.push(`/${link}`);
  }
  return (
    <View
      style={{
        borderRadius: 10,
        backgroundColor: active ? COLORS.lightButton : COLORS.white,
        padding: 7,
        margin: 5,
      }}
    >
      <TouchableOpacity onPress={handleNavigation} disabled={active}>
        <Text style={{ fontWeight: 500, fontSize: 12, padding: 3 }}>
          {buttonTitle}
        </Text>
      </TouchableOpacity>
    </View>
  );
};

export default TabButton;
