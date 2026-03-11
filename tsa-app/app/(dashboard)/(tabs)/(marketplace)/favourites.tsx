import React from "react";
import { View } from "react-native";
import { COLORS, SIZES } from "../../../../constants";
import HeaderSearch from "../../../../components/marketplace/header";
import ListCard from "../../../../components/accessories/ListCard";

const favourites = [
  {
    id: 1,
    title: "MCGP",
    icon: require("../../../../assets/digital/mcgp.png"),
  },
];

const MarketPlace = () => {
  return (
    <View style={{ flex: 1, backgroundColor: COLORS.white }}>
      {/* <HeaderSearch /> */}
      <ListCard itemList={favourites} itemValue="favourites" />
    </View>
  );
};

export default MarketPlace;
