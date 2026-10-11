package helpers

import "os"

// OrDefault returns the value of the environment variable named key, or fallback
// if the variable is unset or empty.
func OrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
