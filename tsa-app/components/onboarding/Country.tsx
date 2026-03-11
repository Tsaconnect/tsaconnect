/* import { CountryPicker } from "@/components/ui/CountryPickerModal/CountryPicker"; // This path will be differ based on your project structure

export default function Country() {
  const [regionCode, setRegionCode] = useState("NG");

  const onSelectCountry = (country) => {
    setRegionCode(country.cca2);
  };

  return (
    <CountryPicker
      onSelect={onSelectCountry}
      countryCode={regionCode}
      withEmoji
      withFilter
    />
  );
}
 */
