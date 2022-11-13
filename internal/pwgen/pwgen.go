package pwgen

import (
	"crypto/rand"
	"math/big"
)

func randomChar() (byte, error) {
	charset := "abcdefghijklmnopqrstuvwxyz" +
		"ABCDEFGHIJKLMNOPQRSTUVWXYZ" +
		"0123456789"

	bn, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
	if err != nil {
		return 0, err
	}
	n := byte(bn.Int64())
	return charset[n], nil
}

func RandomPassword(n int) (string, error) {
	pw := make([]byte, n)
	for i := 0; i < n; i++ {
		c, err := randomChar()
		if err != nil {
			return "", err
		}
		pw[i] = c
	}
	return string(pw), nil
}
