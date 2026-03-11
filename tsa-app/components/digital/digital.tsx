import { ImageBackground, StyleSheet, Text, View } from 'react-native'
import React from 'react'
import DigitalList from './digitalList'
import { SIZES } from '../../constants'

const Digital = () => {
  return (
    <View style={styles.container}>
    <View style={styles.imageContainer}>
      <ImageBackground
        source={require("../../assets/digital/digital.png")}
        style={styles.backgroundImage}
        resizeMode="cover"
      >
        <Text style={styles.text}>Digital</Text>
      </ImageBackground>
    </View>
  
    <DigitalList/>
  </View>
  )
}

export default Digital

const styles = StyleSheet.create({
    container: {
        flex: 1,
      },
      imageContainer: {
        flex: 1,
        flexDirection: "row",
        height:SIZES.height*(0.071888)
      },
      backgroundImage: {
        flex: 1,
        justifyContent: "center", // Aligns children components vertically
        alignItems: "center", // Aligns children components horizontally
      },
      text: {
        color: "white",
        fontSize: 24,
        fontWeight: "600",
      },
})