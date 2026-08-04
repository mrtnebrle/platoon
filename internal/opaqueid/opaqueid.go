package opaqueid

import "strings"

func Valid(value string) bool {
	if value == "" || len(value) > 128 || value == "." || strings.Contains(value, "..") {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '.' || character == '_' || character == '-') {
			return false
		}
	}
	return true
}
