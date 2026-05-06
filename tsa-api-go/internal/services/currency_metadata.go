package services

// CurrencyMeta holds display-only metadata for a fiat currency.
// Rate data is separate (see P2PRate); this table is static and rarely changes.
type CurrencyMeta struct {
	Code   string
	Name   string
	Symbol string
	Flag   string
}

// currencyMetadata covers all ISO 4217 fiat codes returned by Open Exchange Rates.
// Currencies missing from this table are dropped from API responses so the frontend
// can render every code the backend sends without an unknown-code fallback.
var currencyMetadata = map[string]CurrencyMeta{
	// ── Africa ──
	"NGN": {Code: "NGN", Name: "Nigerian Naira", Symbol: "₦", Flag: "🇳🇬"},
	"GHS": {Code: "GHS", Name: "Ghanaian Cedi", Symbol: "GH₵", Flag: "🇬🇭"},
	"KES": {Code: "KES", Name: "Kenyan Shilling", Symbol: "KSh", Flag: "🇰🇪"},
	"ZAR": {Code: "ZAR", Name: "South African Rand", Symbol: "R", Flag: "🇿🇦"},
	"UGX": {Code: "UGX", Name: "Ugandan Shilling", Symbol: "USh", Flag: "🇺🇬"},
	"TZS": {Code: "TZS", Name: "Tanzanian Shilling", Symbol: "TSh", Flag: "🇹🇿"},
	"RWF": {Code: "RWF", Name: "Rwandan Franc", Symbol: "FRw", Flag: "🇷🇼"},
	"XOF": {Code: "XOF", Name: "West African CFA Franc", Symbol: "CFA", Flag: "🌍"},
	"XAF": {Code: "XAF", Name: "Central African CFA Franc", Symbol: "FCFA", Flag: "🌍"},
	"EGP": {Code: "EGP", Name: "Egyptian Pound", Symbol: "E£", Flag: "🇪🇬"},
	"MAD": {Code: "MAD", Name: "Moroccan Dirham", Symbol: "DH", Flag: "🇲🇦"},
	"DZD": {Code: "DZD", Name: "Algerian Dinar", Symbol: "DA", Flag: "🇩🇿"},
	"TND": {Code: "TND", Name: "Tunisian Dinar", Symbol: "د.ت", Flag: "🇹🇳"},
	"LYD": {Code: "LYD", Name: "Libyan Dinar", Symbol: "ل.د", Flag: "🇱🇾"},
	"ETB": {Code: "ETB", Name: "Ethiopian Birr", Symbol: "Br", Flag: "🇪🇹"},
	"SDG": {Code: "SDG", Name: "Sudanese Pound", Symbol: "ج.س.", Flag: "🇸🇩"},
	"SOS": {Code: "SOS", Name: "Somali Shilling", Symbol: "Sh.So.", Flag: "🇸🇴"},
	"DJF": {Code: "DJF", Name: "Djiboutian Franc", Symbol: "Fdj", Flag: "🇩🇯"},
	"ERN": {Code: "ERN", Name: "Eritrean Nakfa", Symbol: "Nfk", Flag: "🇪🇷"},
	"BIF": {Code: "BIF", Name: "Burundian Franc", Symbol: "FBu", Flag: "🇧🇮"},
	"CDF": {Code: "CDF", Name: "Congolese Franc", Symbol: "FC", Flag: "🇨🇩"},
	"AOA": {Code: "AOA", Name: "Angolan Kwanza", Symbol: "Kz", Flag: "🇦🇴"},
	"BWP": {Code: "BWP", Name: "Botswanan Pula", Symbol: "P", Flag: "🇧🇼"},
	"GMD": {Code: "GMD", Name: "Gambian Dalasi", Symbol: "D", Flag: "🇬🇲"},
	"GNF": {Code: "GNF", Name: "Guinean Franc", Symbol: "FG", Flag: "🇬🇳"},
	"KMF": {Code: "KMF", Name: "Comorian Franc", Symbol: "CF", Flag: "🇰🇲"},
	"LRD": {Code: "LRD", Name: "Liberian Dollar", Symbol: "L$", Flag: "🇱🇷"},
	"LSL": {Code: "LSL", Name: "Lesotho Loti", Symbol: "L", Flag: "🇱🇸"},
	"MGA": {Code: "MGA", Name: "Malagasy Ariary", Symbol: "Ar", Flag: "🇲🇬"},
	"MRU": {Code: "MRU", Name: "Mauritanian Ouguiya", Symbol: "UM", Flag: "🇲🇷"},
	"MUR": {Code: "MUR", Name: "Mauritian Rupee", Symbol: "₨", Flag: "🇲🇺"},
	"MWK": {Code: "MWK", Name: "Malawian Kwacha", Symbol: "MK", Flag: "🇲🇼"},
	"MZN": {Code: "MZN", Name: "Mozambican Metical", Symbol: "MT", Flag: "🇲🇿"},
	"NAD": {Code: "NAD", Name: "Namibian Dollar", Symbol: "N$", Flag: "🇳🇦"},
	"SCR": {Code: "SCR", Name: "Seychellois Rupee", Symbol: "SR", Flag: "🇸🇨"},
	"SLE": {Code: "SLE", Name: "Sierra Leonean Leone", Symbol: "Le", Flag: "🇸🇱"},
	"SLL": {Code: "SLL", Name: "Sierra Leonean Leone (old)", Symbol: "Le", Flag: "🇸🇱"},
	"SSP": {Code: "SSP", Name: "South Sudanese Pound", Symbol: "£", Flag: "🇸🇸"},
	"STN": {Code: "STN", Name: "São Tomé & Príncipe Dobra", Symbol: "Db", Flag: "🇸🇹"},
	"SZL": {Code: "SZL", Name: "Eswatini Lilangeni", Symbol: "E", Flag: "🇸🇿"},
	"ZMW": {Code: "ZMW", Name: "Zambian Kwacha", Symbol: "ZK", Flag: "🇿🇲"},
	"ZWL": {Code: "ZWL", Name: "Zimbabwean Dollar", Symbol: "Z$", Flag: "🇿🇼"},
	"CVE": {Code: "CVE", Name: "Cape Verdean Escudo", Symbol: "$", Flag: "🇨🇻"},

	// ── Americas ──
	"USD": {Code: "USD", Name: "US Dollar", Symbol: "$", Flag: "🇺🇸"},
	"CAD": {Code: "CAD", Name: "Canadian Dollar", Symbol: "C$", Flag: "🇨🇦"},
	"MXN": {Code: "MXN", Name: "Mexican Peso", Symbol: "Mex$", Flag: "🇲🇽"},
	"BRL": {Code: "BRL", Name: "Brazilian Real", Symbol: "R$", Flag: "🇧🇷"},
	"ARS": {Code: "ARS", Name: "Argentine Peso", Symbol: "AR$", Flag: "🇦🇷"},
	"CLP": {Code: "CLP", Name: "Chilean Peso", Symbol: "CL$", Flag: "🇨🇱"},
	"COP": {Code: "COP", Name: "Colombian Peso", Symbol: "CO$", Flag: "🇨🇴"},
	"PEN": {Code: "PEN", Name: "Peruvian Sol", Symbol: "S/.", Flag: "🇵🇪"},
	"UYU": {Code: "UYU", Name: "Uruguayan Peso", Symbol: "$U", Flag: "🇺🇾"},
	"VES": {Code: "VES", Name: "Venezuelan Bolívar", Symbol: "Bs.", Flag: "🇻🇪"},
	"BOB": {Code: "BOB", Name: "Bolivian Boliviano", Symbol: "Bs", Flag: "🇧🇴"},
	"PYG": {Code: "PYG", Name: "Paraguayan Guaraní", Symbol: "₲", Flag: "🇵🇾"},
	"GTQ": {Code: "GTQ", Name: "Guatemalan Quetzal", Symbol: "Q", Flag: "🇬🇹"},
	"CRC": {Code: "CRC", Name: "Costa Rican Colón", Symbol: "₡", Flag: "🇨🇷"},
	"NIO": {Code: "NIO", Name: "Nicaraguan Córdoba", Symbol: "C$", Flag: "🇳🇮"},
	"HNL": {Code: "HNL", Name: "Honduran Lempira", Symbol: "L", Flag: "🇭🇳"},
	"PAB": {Code: "PAB", Name: "Panamanian Balboa", Symbol: "B/.", Flag: "🇵🇦"},
	"DOP": {Code: "DOP", Name: "Dominican Peso", Symbol: "RD$", Flag: "🇩🇴"},
	"JMD": {Code: "JMD", Name: "Jamaican Dollar", Symbol: "J$", Flag: "🇯🇲"},
	"BBD": {Code: "BBD", Name: "Barbadian Dollar", Symbol: "Bds$", Flag: "🇧🇧"},
	"BSD": {Code: "BSD", Name: "Bahamian Dollar", Symbol: "B$", Flag: "🇧🇸"},
	"BZD": {Code: "BZD", Name: "Belize Dollar", Symbol: "BZ$", Flag: "🇧🇿"},
	"GYD": {Code: "GYD", Name: "Guyanese Dollar", Symbol: "G$", Flag: "🇬🇾"},
	"SRD": {Code: "SRD", Name: "Surinamese Dollar", Symbol: "Sr$", Flag: "🇸🇷"},
	"TTD": {Code: "TTD", Name: "Trinidad & Tobago Dollar", Symbol: "TT$", Flag: "🇹🇹"},
	"HTG": {Code: "HTG", Name: "Haitian Gourde", Symbol: "G", Flag: "🇭🇹"},
	"CUP": {Code: "CUP", Name: "Cuban Peso", Symbol: "₱", Flag: "🇨🇺"},
	"AWG": {Code: "AWG", Name: "Aruban Florin", Symbol: "ƒ", Flag: "🇦🇼"},
	"ANG": {Code: "ANG", Name: "Netherlands Antillean Guilder", Symbol: "ƒ", Flag: "🇨🇼"},
	"KYD": {Code: "KYD", Name: "Cayman Islands Dollar", Symbol: "CI$", Flag: "🇰🇾"},
	"XCD": {Code: "XCD", Name: "East Caribbean Dollar", Symbol: "EC$", Flag: "🌎"},
	"BMD": {Code: "BMD", Name: "Bermudian Dollar", Symbol: "BD$", Flag: "🇧🇲"},

	// ── Europe ──
	"EUR": {Code: "EUR", Name: "Euro", Symbol: "€", Flag: "🇪🇺"},
	"GBP": {Code: "GBP", Name: "British Pound", Symbol: "£", Flag: "🇬🇧"},
	"CHF": {Code: "CHF", Name: "Swiss Franc", Symbol: "CHF", Flag: "🇨🇭"},
	"NOK": {Code: "NOK", Name: "Norwegian Krone", Symbol: "kr", Flag: "🇳🇴"},
	"SEK": {Code: "SEK", Name: "Swedish Krona", Symbol: "kr", Flag: "🇸🇪"},
	"DKK": {Code: "DKK", Name: "Danish Krone", Symbol: "kr", Flag: "🇩🇰"},
	"ISK": {Code: "ISK", Name: "Icelandic Króna", Symbol: "kr", Flag: "🇮🇸"},
	"PLN": {Code: "PLN", Name: "Polish Złoty", Symbol: "zł", Flag: "🇵🇱"},
	"CZK": {Code: "CZK", Name: "Czech Koruna", Symbol: "Kč", Flag: "🇨🇿"},
	"HUF": {Code: "HUF", Name: "Hungarian Forint", Symbol: "Ft", Flag: "🇭🇺"},
	"RON": {Code: "RON", Name: "Romanian Leu", Symbol: "lei", Flag: "🇷🇴"},
	"BGN": {Code: "BGN", Name: "Bulgarian Lev", Symbol: "лв", Flag: "🇧🇬"},
	"HRK": {Code: "HRK", Name: "Croatian Kuna", Symbol: "kn", Flag: "🇭🇷"},
	"RSD": {Code: "RSD", Name: "Serbian Dinar", Symbol: "дин", Flag: "🇷🇸"},
	"MKD": {Code: "MKD", Name: "Macedonian Denar", Symbol: "ден", Flag: "🇲🇰"},
	"ALL": {Code: "ALL", Name: "Albanian Lek", Symbol: "L", Flag: "🇦🇱"},
	"BAM": {Code: "BAM", Name: "Bosnia & Herzegovina Mark", Symbol: "KM", Flag: "🇧🇦"},
	"MDL": {Code: "MDL", Name: "Moldovan Leu", Symbol: "L", Flag: "🇲🇩"},
	"UAH": {Code: "UAH", Name: "Ukrainian Hryvnia", Symbol: "₴", Flag: "🇺🇦"},
	"RUB": {Code: "RUB", Name: "Russian Ruble", Symbol: "₽", Flag: "🇷🇺"},
	"BYN": {Code: "BYN", Name: "Belarusian Ruble", Symbol: "Br", Flag: "🇧🇾"},
	"GEL": {Code: "GEL", Name: "Georgian Lari", Symbol: "₾", Flag: "🇬🇪"},
	"AMD": {Code: "AMD", Name: "Armenian Dram", Symbol: "֏", Flag: "🇦🇲"},
	"AZN": {Code: "AZN", Name: "Azerbaijani Manat", Symbol: "₼", Flag: "🇦🇿"},
	"TRY": {Code: "TRY", Name: "Turkish Lira", Symbol: "₺", Flag: "🇹🇷"},
	"GIP": {Code: "GIP", Name: "Gibraltar Pound", Symbol: "£", Flag: "🇬🇮"},
	"GGP": {Code: "GGP", Name: "Guernsey Pound", Symbol: "£", Flag: "🇬🇬"},
	"IMP": {Code: "IMP", Name: "Isle of Man Pound", Symbol: "£", Flag: "🇮🇲"},
	"JEP": {Code: "JEP", Name: "Jersey Pound", Symbol: "£", Flag: "🇯🇪"},
	"FKP": {Code: "FKP", Name: "Falkland Islands Pound", Symbol: "£", Flag: "🇫🇰"},
	"SHP": {Code: "SHP", Name: "Saint Helena Pound", Symbol: "£", Flag: "🇸🇭"},

	// ── Middle East ──
	"AED": {Code: "AED", Name: "UAE Dirham", Symbol: "د.إ", Flag: "🇦🇪"},
	"SAR": {Code: "SAR", Name: "Saudi Riyal", Symbol: "﷼", Flag: "🇸🇦"},
	"QAR": {Code: "QAR", Name: "Qatari Riyal", Symbol: "ر.ق", Flag: "🇶🇦"},
	"KWD": {Code: "KWD", Name: "Kuwaiti Dinar", Symbol: "د.ك", Flag: "🇰🇼"},
	"BHD": {Code: "BHD", Name: "Bahraini Dinar", Symbol: ".د.ب", Flag: "🇧🇭"},
	"OMR": {Code: "OMR", Name: "Omani Rial", Symbol: "ر.ع.", Flag: "🇴🇲"},
	"ILS": {Code: "ILS", Name: "Israeli New Shekel", Symbol: "₪", Flag: "🇮🇱"},
	"JOD": {Code: "JOD", Name: "Jordanian Dinar", Symbol: "د.أ", Flag: "🇯🇴"},
	"LBP": {Code: "LBP", Name: "Lebanese Pound", Symbol: "ل.ل", Flag: "🇱🇧"},
	"IQD": {Code: "IQD", Name: "Iraqi Dinar", Symbol: "ع.د", Flag: "🇮🇶"},
	"IRR": {Code: "IRR", Name: "Iranian Rial", Symbol: "﷼", Flag: "🇮🇷"},
	"YER": {Code: "YER", Name: "Yemeni Rial", Symbol: "﷼", Flag: "🇾🇪"},
	"SYP": {Code: "SYP", Name: "Syrian Pound", Symbol: "£S", Flag: "🇸🇾"},
	"AFN": {Code: "AFN", Name: "Afghan Afghani", Symbol: "؋", Flag: "🇦🇫"},

	// ── Asia ──
	"JPY": {Code: "JPY", Name: "Japanese Yen", Symbol: "¥", Flag: "🇯🇵"},
	"CNY": {Code: "CNY", Name: "Chinese Yuan", Symbol: "¥", Flag: "🇨🇳"},
	"HKD": {Code: "HKD", Name: "Hong Kong Dollar", Symbol: "HK$", Flag: "🇭🇰"},
	"TWD": {Code: "TWD", Name: "New Taiwan Dollar", Symbol: "NT$", Flag: "🇹🇼"},
	"KRW": {Code: "KRW", Name: "South Korean Won", Symbol: "₩", Flag: "🇰🇷"},
	"KPW": {Code: "KPW", Name: "North Korean Won", Symbol: "₩", Flag: "🇰🇵"},
	"INR": {Code: "INR", Name: "Indian Rupee", Symbol: "₹", Flag: "🇮🇳"},
	"PKR": {Code: "PKR", Name: "Pakistani Rupee", Symbol: "₨", Flag: "🇵🇰"},
	"BDT": {Code: "BDT", Name: "Bangladeshi Taka", Symbol: "৳", Flag: "🇧🇩"},
	"LKR": {Code: "LKR", Name: "Sri Lankan Rupee", Symbol: "Rs", Flag: "🇱🇰"},
	"NPR": {Code: "NPR", Name: "Nepalese Rupee", Symbol: "Rs", Flag: "🇳🇵"},
	"BTN": {Code: "BTN", Name: "Bhutanese Ngultrum", Symbol: "Nu.", Flag: "🇧🇹"},
	"MVR": {Code: "MVR", Name: "Maldivian Rufiyaa", Symbol: "Rf", Flag: "🇲🇻"},
	"IDR": {Code: "IDR", Name: "Indonesian Rupiah", Symbol: "Rp", Flag: "🇮🇩"},
	"MYR": {Code: "MYR", Name: "Malaysian Ringgit", Symbol: "RM", Flag: "🇲🇾"},
	"PHP": {Code: "PHP", Name: "Philippine Peso", Symbol: "₱", Flag: "🇵🇭"},
	"SGD": {Code: "SGD", Name: "Singapore Dollar", Symbol: "S$", Flag: "🇸🇬"},
	"THB": {Code: "THB", Name: "Thai Baht", Symbol: "฿", Flag: "🇹🇭"},
	"VND": {Code: "VND", Name: "Vietnamese Dong", Symbol: "₫", Flag: "🇻🇳"},
	"MMK": {Code: "MMK", Name: "Myanmar Kyat", Symbol: "K", Flag: "🇲🇲"},
	"KHR": {Code: "KHR", Name: "Cambodian Riel", Symbol: "៛", Flag: "🇰🇭"},
	"LAK": {Code: "LAK", Name: "Lao Kip", Symbol: "₭", Flag: "🇱🇦"},
	"MNT": {Code: "MNT", Name: "Mongolian Tögrög", Symbol: "₮", Flag: "🇲🇳"},
	"KZT": {Code: "KZT", Name: "Kazakhstani Tenge", Symbol: "₸", Flag: "🇰🇿"},
	"UZS": {Code: "UZS", Name: "Uzbekistani Som", Symbol: "сўм", Flag: "🇺🇿"},
	"KGS": {Code: "KGS", Name: "Kyrgystani Som", Symbol: "с", Flag: "🇰🇬"},
	"TJS": {Code: "TJS", Name: "Tajikistani Somoni", Symbol: "ЅМ", Flag: "🇹🇯"},
	"TMT": {Code: "TMT", Name: "Turkmenistani Manat", Symbol: "T", Flag: "🇹🇲"},
	"BND": {Code: "BND", Name: "Brunei Dollar", Symbol: "B$", Flag: "🇧🇳"},
	"MOP": {Code: "MOP", Name: "Macanese Pataca", Symbol: "MOP$", Flag: "🇲🇴"},

	// ── Pacific ──
	"AUD": {Code: "AUD", Name: "Australian Dollar", Symbol: "A$", Flag: "🇦🇺"},
	"NZD": {Code: "NZD", Name: "New Zealand Dollar", Symbol: "NZ$", Flag: "🇳🇿"},
	"FJD": {Code: "FJD", Name: "Fijian Dollar", Symbol: "FJ$", Flag: "🇫🇯"},
	"PGK": {Code: "PGK", Name: "Papua New Guinean Kina", Symbol: "K", Flag: "🇵🇬"},
	"SBD": {Code: "SBD", Name: "Solomon Islands Dollar", Symbol: "SI$", Flag: "🇸🇧"},
	"TOP": {Code: "TOP", Name: "Tongan Paʻanga", Symbol: "T$", Flag: "🇹🇴"},
	"VUV": {Code: "VUV", Name: "Vanuatu Vatu", Symbol: "Vt", Flag: "🇻🇺"},
	"WST": {Code: "WST", Name: "Samoan Tala", Symbol: "WS$", Flag: "🇼🇸"},
	"XPF": {Code: "XPF", Name: "CFP Franc", Symbol: "₣", Flag: "🇵🇫"},
	"KID": {Code: "KID", Name: "Kiribati Dollar", Symbol: "$", Flag: "🇰🇮"},
	"TVD": {Code: "TVD", Name: "Tuvaluan Dollar", Symbol: "$", Flag: "🇹🇻"},
}

// currencyMeta returns metadata for a currency code, falling back to a code-only entry.
func currencyMeta(code string) CurrencyMeta {
	if m, ok := currencyMetadata[code]; ok {
		return m
	}
	return CurrencyMeta{Code: code, Name: code, Symbol: code}
}

// isKnownCurrency reports whether a currency is listed in our metadata table.
// Used to filter out non-fiat codes that OER returns (e.g. precious metals).
func isKnownCurrency(code string) bool {
	_, ok := currencyMetadata[code]
	return ok
}
