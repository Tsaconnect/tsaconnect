import React from 'react';
import { View, StyleSheet} from 'react-native';
import { Text, Card, Button } from 'react-native-elements';
import { COLORS, SIZES } from '../../constants';
import { router } from 'expo-router';
const CardProduct=({item}) => {
  function handleBuy(){
router.push({pathname:'/orderproduct',params:{value:item}})
  }
return (
  <>
        <Card containerStyle={{width:SIZES.width*(0.468695652),borderTopLeftRadius:15,borderTopRightRadius:15,margin:'0.75%'}}>
          <Card.Image
            style={{ padding: 0,height:SIZES.height*(0.1395),width:'100%' }}
            source={{
              uri:item.images[0]
            }}
            resizeMode='contain'
          />
          <Text style={{ marginBottom: 10 }}>
            {item.title}
          </Text>
          <View style={{justifyContent:'space-between',flexDirection:'row',alignItems:'center'}}>
         <Text onPress={()=>{
        
         }} style={{fontSize:13,fontWeight:'600'}}>Catalogue</Text>
          <Button
          onPress={handleBuy}
            buttonStyle={{
              borderRadius: 30,
              marginLeft: 2,
              marginRight: 0,
              marginBottom: 0,
              padding:4,
              height:SIZES.height*(0.04506),
              backgroundColor:'#E8A14A'
            }}
     
            title="Buy Now"
            titleStyle={{
                color:COLORS.black
            }}
          />
          </View>
        </Card>
  </>
);
};

const styles = StyleSheet.create({
container: {
  flex: 1,
},
fonts: {
  marginBottom: 8,
},
user: {
  flexDirection: 'row',
  marginBottom: 6,
},
image: {
  width: 30,
  height: 30,
  marginRight: 10,
},
name: {
  fontSize: 16,
  marginTop: 5,
},
});

export default CardProduct;