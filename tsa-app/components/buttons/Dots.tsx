import { View, Text } from "react-native";
import React from "react";
import { COLORS } from "../../constants/theme";

const Dots = () => {
  return (
    <View
      style={{
        backgroundColor: COLORS.gray,
        width: 10,
        height: 10,
        borderRadius: 50 / 2,
        margin: 5,
      }}
    ></View>
  );
};

export default Dots;
