import { View, Text, TouchableOpacity, Image } from "react-native";
import { FONTS, SIZES, assets } from "../../constants";;
const HomeHeader = () => {

    const openNavBar=()=>{
        //navigation.openDrawer()
    }
  return (
  
      <View
        style={{
            flexDirection:'row',
            height:SIZES.height*(0.06901),
            justifyContent:'space-between',
            marginHorizontal:'4%',
            width:'92%',
            marginTop:SIZES.height*(0.050429),
          /*   backgroundColor:COLORS.white */
        }}
      >
        <TouchableOpacity onPress={openNavBar}>
          <Image
            source={assets.logo}
            resizeMode="contain"
            style={{ width: 45, height: 25,marginTop:5}}
          />
        </TouchableOpacity>

        <View
          style={{
            width: 45,
            height: 45,
          }}
        >
          <Image
            source={assets.person02}
            resizeMode="contain"
            style={{
              width: "100%",
              height: "100%",
            }}
          />
          <Image
            source={assets.badge}
            resizeMode="contain"
            style={{
              position: "absolute",
              width: 15,
              height: 15,
              bottom: 0,
              right: 0,
            }}
          />
        </View>
      </View>
  );
};

export default HomeHeader;
