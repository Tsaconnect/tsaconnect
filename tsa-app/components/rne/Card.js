import React from 'react';
import { View, ScrollView, StyleSheet, Image } from 'react-native';
import { Text, Card, Button, Icon } from 'react-native-elements';
import { COLORS, SIZES } from '../../constants';

const users = [
{
  name: 'brynn',
  avatar: 'https://uifaces.co/our-content/donated/1H_7AxP0.jpg',
},
{
  name: 'thot leader',
  avatar:
    'https://images.pexels.com/photos/598745/pexels-photo-598745.jpeg?crop=faces&fit=crop&h=200&w=200&auto=compress&cs=tinysrgb',
},
{
  name: 'jsa',
  avatar: 'https://uifaces.co/our-content/donated/bUkmHPKs.jpg',
},
{
  name: 'talhaconcepts',
  avatar: 'https://randomuser.me/api/portraits/men/4.jpg',
},
{
  name: 'andy vitale',
  avatar: 'https://uifaces.co/our-content/donated/NY9hnAbp.jpg',
},
{
  name: 'katy friedson',
  avatar:
    'https://images-na.ssl-images-amazon.com/images/M/MV5BMTgxMTc1MTYzM15BMl5BanBnXkFtZTgwNzI5NjMwOTE@._V1_UY256_CR16,0,172,256_AL_.jpg',
},
];
const Cards=({image,title}) => {
return (
  <>
    <ScrollView>
      <View style={styles.container}>
        <Card containerStyle={{width:'48%',borderTopLeftRadius:15,borderTopRightRadius:15}}>
          <Card.Image
            style={{ padding: 0,height:SIZES.height*(0.1395),width:'100' }}
            source={{
              uri:
                'https://awildgeographer.files.wordpress.com/2015/02/john_muir_glacier.jpg',
            }}
          />
          <Text style={{ marginBottom: 10 }}>
            The idea with React
          </Text>
          <View style={{justifyContent:'space-between',flexDirection:'row',alignItems:'center'}}>
         <Text onPress={()=>{
        
         }} style={{fontSize:11,fontWeight:'500'}}>See Catalogue</Text>
          <Button
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
      </View>
    </ScrollView>
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

export default Cards;