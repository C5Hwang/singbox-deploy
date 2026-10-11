// Package subscription generates subscription outputs and aggregates remote
// same-version subscriptions.
package subscription

import (
	"crypto/md5"
	"encoding/hex"
	"strings"
)

// TokenFromSalt derives the subscription URL token from a salt. The trailing
// newline is part of the hashed input and must match the remote convention so
// that same-version aggregation computes identical tokens. The salt is trimmed
// first, as the dashboard and AuthKeyCipherKey trim it.
func TokenFromSalt(salt string) string {
	sum := md5.Sum([]byte(strings.TrimSpace(salt) + "\n"))
	return hex.EncodeToString(sum[:])
}
