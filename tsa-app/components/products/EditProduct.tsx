import React, { useState, useEffect } from "react";
import {
  View,
  Text,
  TextInput,
  Button,
  ScrollView,
  StyleSheet,
  TouchableOpacity,
} from "react-native";
import { useForm, Controller, useFieldArray } from "react-hook-form";
import axios from "axios";
import { useNavigation, useRoute } from "@react-navigation/native";
import { showMessage } from "react-native-flash-message";
import { baseUrl } from "../../constants/api/apiClient";

const EditProductCatalogue = () => {
  const navigation = useNavigation();
  const route = useRoute();
  const { id } = route.params;
  const [brands, setBrands] = useState([]);
  const [categories, setCategories] = useState([]);
  const [product, setProduct] = useState({});
  const [initialProduct, setInitialProduct] = useState({});

  const {
    control,
    handleSubmit,
    formState: { isSubmitting },
  } = useForm();

  const { fields, append, remove } = useFieldArray({
    control,
    name: "variations",
  });

  const fetchProductsCategories = async () => {
    try {
      const response = await axios.get(`${baseUrl}/category?type=Product`, {
        headers: {
          Authorization: `${token}`,
        },
      }); // Adjust the endpoint based on your API
      const fetchedCategories = response.data.results;
      setCategories(fetchedCategories);
    } catch (error) {}
  };

  const fetchProductsBrands = async () => {
    const response = await axios.get("/stores/brands");
    setBrands(response.data);
  };

  useEffect(() => {
    fetchProductsCategories();
    fetchProductsBrands();
  }, []);

  useEffect(() => {
    const fetchProductCatalogue = async () => {
      try {
        const response = await axios.get(`/products/catalogue/${id}`);
        setProduct(response.data);
        setInitialProduct(response.data);
      } catch (error) {
        console.error(error);
      }
    };

    fetchProductCatalogue();
  }, [id]);

  const updateField = (field, value) => {
    setProduct((prev) => ({ ...prev, [field]: value }));
  };

  const submitHandler = async (productData) => {
    try {
      const changedValues = Object.entries(product)
        .filter(([key, value]) => value !== initialProduct[key])
        .reduce((obj, [key, value]) => {
          obj[key] = value;
          return obj;
        }, {});

      await axios.patch(`${baseUrl}/products/catalogue/${id}`, changedValues);
      showMessage({
        message: "Product catalogue updated successfully!",
        type: "success",
      });
      navigation.navigate("CatalogueDetails", { id });
    } catch (error) {
      if (
        error.response &&
        error.response.data &&
        error.response.data.message
      ) {
        showMessage({
          message: error.response.data.message,
          type: "danger",
        });
      } else {
        showMessage({
          message: "Something went wrong. Please try again later.",
          type: "danger",
        });
      }
    }
  };

  return (
    <ScrollView style={styles.container}>
      <TouchableOpacity onPress={() => navigation.goBack()}>
        <Text style={styles.backButton}>Back</Text>
      </TouchableOpacity>
      <View style={styles.formContainer}>
        <Controller
          control={control}
          name="name"
          defaultValue={product.name || ""}
          render={({ field: { onChange, value } }) => (
            <View style={styles.formControl}>
              <Text style={styles.label}>Name</Text>
              <TextInput
                style={styles.input}
                placeholder="Enter Product Name"
                value={value}
                onChangeText={onChange}
              />
            </View>
          )}
        />

        <Controller
          control={control}
          name="description"
          defaultValue={product.description || ""}
          render={({ field: { onChange, value } }) => (
            <View style={styles.formControl}>
              <Text style={styles.label}>Description</Text>
              <TextInput
                style={styles.textarea}
                placeholder="Enter Product Description"
                value={value}
                onChangeText={onChange}
                multiline
              />
            </View>
          )}
        />

        {fields.map((field, index) => (
          <View key={field.id} style={styles.variationContainer}>
            <Controller
              control={control}
              name={`variations[${index}].name`}
              defaultValue={field.name || ""}
              render={({ field: { onChange, value } }) => (
                <TextInput
                  style={styles.input}
                  placeholder="Variation Name"
                  value={value}
                  onChangeText={onChange}
                />
              )}
            />
            <Controller
              control={control}
              name={`variations[${index}].value`}
              defaultValue={field.value || ""}
              render={({ field: { onChange, value } }) => (
                <TextInput
                  style={styles.input}
                  placeholder="Variation Values (comma-separated)"
                  value={value}
                  onChangeText={onChange}
                />
              )}
            />
            <Button title="Remove" onPress={() => remove(index)} />
          </View>
        ))}

        <Button
          title="Add Variation"
          onPress={() => append({ name: "", value: "" })}
        />

        <View style={styles.formControl}>
          <Text style={styles.label}>Brand</Text>
          <Controller
            control={control}
            name="brand"
            defaultValue={product.brand || ""}
            render={({ field: { onChange, value } }) => (
              <Picker
                selectedValue={value}
                onValueChange={(itemValue) => onChange(itemValue)}
              >
                <Picker.Item label="Select Product Brand" value="" />
                {brands.map((brand) => (
                  <Picker.Item
                    key={brand.id}
                    label={brand.name}
                    value={brand.id}
                  />
                ))}
              </Picker>
            )}
          />
        </View>

        <View style={styles.formControl}>
          <Text style={styles.label}>Category</Text>
          <Controller
            control={control}
            name="categoryId"
            defaultValue={product.category || ""}
            render={({ field: { onChange, value } }) => (
              <Picker
                selectedValue={value}
                onValueChange={(itemValue) => onChange(itemValue)}
              >
                <Picker.Item label="Select Product Category" value="" />
                {categories.map((category) => (
                  <Picker.Item
                    key={category.id}
                    label={category.title}
                    value={category.id}
                  />
                ))}
              </Picker>
            )}
          />
        </View>

        <Controller
          control={control}
          name="costPrice"
          defaultValue={product.costPrice || ""}
          render={({ field: { onChange, value } }) => (
            <View style={styles.formControl}>
              <Text style={styles.label}>Cost Price</Text>
              <TextInput
                style={styles.input}
                placeholder="Enter Product Cost Price"
                value={value}
                onChangeText={onChange}
                keyboardType="numeric"
              />
            </View>
          )}
        />

        <Controller
          control={control}
          name="sellingPrice"
          defaultValue={product.sellingPrice || ""}
          render={({ field: { onChange, value } }) => (
            <View style={styles.formControl}>
              <Text style={styles.label}>Selling Price</Text>
              <TextInput
                style={styles.input}
                placeholder="Enter Product Sales Price"
                value={value}
                onChangeText={onChange}
                keyboardType="numeric"
              />
            </View>
          )}
        />

        <Controller
          control={control}
          name="discount"
          defaultValue={product.discount || ""}
          render={({ field: { onChange, value } }) => (
            <View style={styles.formControl}>
              <Text style={styles.label}>Discount Price</Text>
              <TextInput
                style={styles.input}
                placeholder="Enter Product Discount Price"
                value={value}
                onChangeText={onChange}
                keyboardType="numeric"
              />
            </View>
          )}
        />

        <Controller
          control={control}
          name="quantity"
          defaultValue={product.quantity || ""}
          render={({ field: { onChange, value } }) => (
            <View style={styles.formControl}>
              <Text style={styles.label}>Quantity</Text>
              <TextInput
                style={styles.input}
                placeholder="Enter Product Quantity"
                value={value}
                onChangeText={onChange}
                keyboardType="numeric"
              />
            </View>
          )}
        />

        <Button
          title="Submit"
          onPress={handleSubmit(submitHandler)}
          disabled={isSubmitting}
        />
      </View>
    </ScrollView>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
    padding: 16,
    backgroundColor: "#fff",
  },
  backButton: {
    color: "blue",
    marginBottom: 16,
  },
  formContainer: {
    width: "100%",
  },
  formControl: {
    marginBottom: 16,
  },
  label: {
    marginBottom: 8,
    fontWeight: "500",
  },
  input: {
    borderWidth: 1,
    borderColor: "#ccc",
    padding: 8,
    borderRadius: 4,
  },
  textarea: {
    borderWidth: 1,
    borderColor: "#ccc",
    padding: 8,
    borderRadius: 4,
    height: 100,
  },
  variationContainer: {
    marginBottom: 16,
  },
});

export default EditProductCatalogue;
