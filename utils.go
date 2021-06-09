package posthog

import (
	"crypto/sha1"
	"strconv"
	"fmt"
)


func checkIfSimpleFlagEnabled(key string, distinctId string, rolloutPercentage uint8) (bool, error) {
	hash := sha1.New()
	hash.Write([]byte("" + key + "." + distinctId + ""))
	digest := hash.Sum(nil)
	hexString := fmt.Sprintf("%x\n", digest)[:15]

	value, err := strconv.ParseInt(hexString, 16, 64)
	if err != nil {
		return false, err
	}
	return (float64(value) / LONG_SCALE) <= float64(rolloutPercentage)/100, nil
}