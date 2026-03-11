import { ScrollView, StyleSheet, Text, View } from 'react-native'
import React from 'react'
import AppServiceList from '../../../../components/appservices/AppServices'
import ServicesScreen from '@/screens/service'

/* const data=[
  {id:1,title:'Car mechanics service',description:`Auto mechanics inspect cars, maintain vehicles and fix car problems to get them back on the road for safe operation for our clients.`, 
  category:'Mechanics',
  image:require('../../../../assets/services/mechanic.jpg')},
  {id:2,title:'Catering service',
  description:`Our catering service is about preparing food and providing food services for clients at remote locations,such as hotels, restaurants, offices, concerts, and events.`,
  category:'Mechanics',
  image:require('../../../../assets/services/caterings.png')}
] */
const Services = () => {
  return (
    <>
    <ScrollView>
      {/*  {data.map((item, index) => (
      <ServiceCard key={index} title={item.title} description={item.description} image={item.image}/>
       ))} */}
<AppServiceList/>
{/* <ServicesScreen/> */}
    </ScrollView>
    </>
  )
}

export default Services

const styles = StyleSheet.create({})