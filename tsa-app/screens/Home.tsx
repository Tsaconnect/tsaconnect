import { StyleSheet, ScrollView, View, Text } from "react-native";
import React from "react";
//import HomeHeader from '../components/headers/HomeHeader'
import { assets, COLORS, SIZES } from "../constants";
import NavTab from "../components/buttons/NavTab";
import Card from "../components/features/Card";
import Products from "../components/products/Products";
import Services from "../components/services/Services";
import Digital from "../components/digital/digital";

const HomeScreen = () => {
  return (
    <>
      {/*  <HomeHeader/> */}
      <ScrollView style={styles.content}>
        <View
          style={{
            flexDirection: "row",
            height: SIZES.height * 0.05901,
            justifyContent: "space-between",
            marginHorizontal: "4%",
            width: "92%",
            marginTop: 10,
          }}
        >
          <NavTab title={"Deposit"} handlPress={() => {}} />
          <NavTab title={"Withdrawal"} handlPress={() => {}} />
        </View>
        <View style={styles.features}>
          <View>
            <Card
              iconName="swap-horiz"
              title="Send/receive money across borders"
            />
            <Card
              iconName="swap-horizontal-circle"
              title="Buy and sell Cypto
(digital assets)
like bitcoin,
Eth,USDT,
Mcgp and other "
              buttonTitle="Trade"
            />
            <Card
              imageUrl={assets.spin}
              iconName="swap-horizontal-circle"
              title="Spin to earn"
              buttonTitle="Check Games Now"
            />
            <Card
              imageUrl={assets.mining}
              title="Mining pool:
    Mine & earn MCGP for free"
              buttonTitle="Start Mining"
            />
          </View>
          <View>
            <Card
              iconName="sell"
              title="Buy and sell
product and service."
              buttonTitle="Register / Trade"
            />
            <Card
              iconName="token"
              title="Tokenize your business-
    projects and product"
              buttonTitle="Get Started"
            />
            <Card
              iconName="payments"
              title="Pay for tv cables sub
    / other utilities & fees"
              buttonTitle="Pay"
            />
            <Card
              title="Digital earning: stake
    your usdt to earn free
    mCgp assets"
              buttonTitle="Stake here"
            />
          </View>
        </View>
        <Products />
        <Services />
        <Digital />
      </ScrollView>
      {/*  <HomeHeader navigation={navigation}/> */}
    </>
  );
};

export default HomeScreen;

const styles = StyleSheet.create({
  content: {
    flex: 1,
    backgroundColor: COLORS.white,
  },
  features: {
    flexDirection: "row",
    justifyContent: "space-between",
    marginHorizontal: "4%",
    width: "92%",
    marginTop: 10,
  },
});
