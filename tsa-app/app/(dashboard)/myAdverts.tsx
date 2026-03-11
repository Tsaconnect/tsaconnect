import React, { useState, useEffect } from "react";
import {
  StyleSheet,
  View,
  Text,
  TouchableOpacity,
  Image,
  FlatList,
  Alert,
  ActivityIndicator,
} from "react-native";
import { Card, Icon } from "react-native-elements";
import { useAuth } from "../../AuthContext/AuthContext";
import { Link, router } from "expo-router";
import axios from "axios";
import { baseUrl } from "../../constants/api/apiClient";
import { ADVERT_TYPE_PRODUCT } from "../../constants/constantValues";

const MyAdvertsScreen = () => {
  const { token, setCurrentUser } = useAuth();
  const [adverts, setAdverts] = useState([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    fetchAdverts();
  }, []);

  const fetchAdverts = async () => {
    ///users/me
    try {
      const { data } = await axios.get(
        `${baseUrl}/users/me`,

        {
          headers: {
            Authorization: `${token}`,
          },
        }
      );
      setCurrentUser(data);
      const response = await axios.get(
        `${baseUrl}/adverts?createdBy=${data.id}`,
        {
          method: "GET",
          headers: {
            "Content-Type": "application/json",
            Authorization: `${token}`,
          },
        }
      );
      setAdverts(response.data.results);
    } catch (error) {
      console.error("Error fetching adverts:", error);
      Alert.alert("Error", "Failed to fetch adverts. Please try again later.");
    } finally {
      setLoading(false);
    }
  };

  const handleDelete = (id: any) => {
    Alert.alert(
      "Delete Advert",
      "Are you sure you want to delete this advert?",
      [
        {
          text: "Cancel",
          style: "cancel",
        },
        {
          text: "Delete",
          onPress: async () => {
            try {
              await axios.delete(`${baseUrl}/adverts/${id}`, {
                headers: {
                  "Content-Type": "application/json",
                  Authorization: `${token}`,
                },
              });
              fetchAdverts();
            } catch (error) {
              console.error("Error deleting advert:", error);
              Alert.alert(
                "Error",
                "Failed to delete advert. Please try again later."
              );
            }
          },
          style: "destructive",
        },
      ],
      { cancelable: true }
    );
  };

  const renderItem = ({ item }: { item: any }) => (
    <Card containerStyle={styles.card}>
      <Image
        source={{ uri: item.featuredImage || item.images[0] }}
        style={styles.image}
        resizeMode="contain"
      />
      <View style={styles.infoContainer}>
        <Text style={styles.title}>{item.name}</Text>
        {item.type === ADVERT_TYPE_PRODUCT && (
          <Text style={styles.amount}>{item.price}</Text>
        )}
        <Text style={[styles.status, item.status === 'Rejected' && styles.statusRejected]}>
          {item.status}
        </Text>
        {item.status === 'Rejected' && (
          <Text style={styles.rejectionReason}>
            Reason: {item.rejectionReason || "Please contact admin for details"}
          </Text>
        )}
        <Text style={styles.status}>{item.type}</Text>
        <Text style={styles.date}>
          {new Date(item.createdAt).toLocaleDateString()}
        </Text>
        <View style={styles.actions}>
          <TouchableOpacity
            onPress={() =>
              router.push({ pathname: `/profile/edit-advert`, params: item })
            }
          >
            <Icon name="edit" size={20} color="#9D6B38" />
          </TouchableOpacity>
          <TouchableOpacity onPress={() => handleDelete(item.id)}>
            <Icon name="delete" size={20} color="#9D6B38" />
          </TouchableOpacity>
          <TouchableOpacity
            onPress={() =>
              router.push({
                pathname: `/profile/advert-details`,
                params: { id: item.id },
              })
            }
          >
            <Icon name="info" size={20} color="#9D6B38" />
          </TouchableOpacity>
        </View>
      </View>
    </Card>
  );

  if (loading) {
    return (
      <View style={styles.loadingContainer}>
        <ActivityIndicator size="large" color="#9D6B38" />
      </View>
    );
  }

  return (
    <View style={styles.container}>
      <FlatList
        data={adverts}
        keyExtractor={(item) => item.id.toString()}
        renderItem={renderItem}
        ListEmptyComponent={
          <Text style={styles.info}>
            No adverts found.{" "}
            <TouchableOpacity
              onPress={() => {
                router.push({
                  pathname: "/serviceaction",
                  params: { index: 0 },
                });
              }}
            >
              <Text style={styles.registerLink}>Register</Text>
            </TouchableOpacity>
          </Text>
        }
      />
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: "#f0f0f0",
    padding: 10,
  },
  card: {
    borderRadius: 10,
    padding: 20,
    marginVertical: 10,
    backgroundColor: "#F5FCFF",
  },
  image: {
    width: "100%",
    height: 150,
    borderRadius: 10,
  },
  infoContainer: {
    marginTop: 10,
  },
  title: {
    fontSize: 18,
    fontWeight: "bold",
  },
  amount: {
    fontSize: 16,
    color: "#666",
  },
  status: {
    fontSize: 14,
    color: "#666",
  },
  date: {
    fontSize: 12,
    color: "#999",
  },
  actions: {
    flexDirection: "row",
    justifyContent: "space-around",
    marginTop: 10,
  },
  loadingContainer: {
    flex: 1,
    justifyContent: "center",
    alignItems: "center",
  },
  registerLink: {
    fontSize: 16,
    color: "#E8A14A",
    marginTop: 20,
  },
  registerLinkText: {
    marginTop: 15,
    fontSize: 16,
    color: "#E8A14A",
  },
  statusRejected: {
    color: "red",
    fontWeight: "bold",
  },
  rejectionReason: {
    color: "red",
    marginTop: 4,
    fontSize: 12,
    fontStyle: "italic",
  },
  info: {
    textAlign: 'center',
    marginTop: 20,
    fontSize: 16,
    color: '#666',
  },
});

export default MyAdvertsScreen;
