package utils

import (
	"regexp"
	"unicode"
)

// PasswordStrength describes the strength characteristics of a password.
type PasswordStrength struct {
	IsValid     bool `json:"isValid"`
	Length      bool `json:"length"`
	UpperCase   bool `json:"upperCase"`
	LowerCase   bool `json:"lowerCase"`
	Numbers     bool `json:"numbers"`
	SpecialChar bool `json:"specialChar"`
}

var (
	bvnRegex   = regexp.MustCompile(`^\d{11}$`)
	phoneRegex = regexp.MustCompile(`^\+?[1-9]\d{6,14}$`)
	emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)
)

// IsValidBVN checks if the given string is a valid 11-digit BVN.
func IsValidBVN(bvn string) bool {
	return bvnRegex.MatchString(bvn)
}

// IsValidPhoneNumber checks if the given string is a valid phone number.
func IsValidPhoneNumber(phone string) bool {
	return phoneRegex.MatchString(phone)
}

// IsStrongPassword checks password strength and returns the result with details.
func IsStrongPassword(password string) (bool, PasswordStrength) {
	strength := PasswordStrength{
		Length: len(password) >= 8,
	}

	for _, ch := range password {
		switch {
		case unicode.IsUpper(ch):
			strength.UpperCase = true
		case unicode.IsLower(ch):
			strength.LowerCase = true
		case unicode.IsDigit(ch):
			strength.Numbers = true
		case unicode.IsPunct(ch) || unicode.IsSymbol(ch):
			strength.SpecialChar = true
		}
	}

	strength.IsValid = strength.Length && strength.UpperCase && strength.LowerCase && strength.Numbers && strength.SpecialChar

	return strength.IsValid, strength
}

// IsValidEmail checks if the given string is a valid email address.
func IsValidEmail(email string) bool {
	return emailRegex.MatchString(email)
}
