// Package subscription generates subscription outputs and aggregates remote
// same-version subscriptions.
package subscription

import (
	"crypto/md5"
	"encoding/hex"
)

// TokenFromSalt derives the subscription URL token from a salt. The trailing
// newline is part of the hashed input and must match the remote convention so
// that same-version aggregation computes identical tokens.
func TokenFromSalt(salt string) string {
	sum := md5.Sum([]byte(salt + "\n"))
	return hex.EncodeToString(sum[:])
}
