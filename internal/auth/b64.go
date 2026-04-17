package auth

import "encoding/base64"

func decodeStdB64(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}
