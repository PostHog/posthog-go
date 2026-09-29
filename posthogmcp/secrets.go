package posthogmcp

import (
	"math"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A port of posthog-python's exception_utils._looks_like_secret: entropy,
// known vendor formats, and PEM markers, tuned so ids, digests, paths and
// prose stay readable. Lengths count code points, as Python's len does.
const (
	secretMinLength          = 16
	secretMinEntropyBits     = 3.8
	secretMinCharClasses     = 3
	knownSecretMaxScanLength = 200
	pemPrivateKeyMarker      = "PRIVATE KEY-----"
	// Punctuation seen in object reprs and structured strings but never in a
	// bare token.
	secretRejectChars = "()[]{}<>'\"`,;"
)

var (
	uuidPattern     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	pathWordPattern = regexp.MustCompile(`^[a-z][a-z.]*$`)

	knownSecretPattern = regexp.MustCompile(strings.Join([]string{
		`sk-ant-[A-Za-z0-9_-]{16,}`,
		`sk-(?:proj-)?[A-Za-z0-9_-]{20,}`,
		`hf_[A-Za-z0-9]{34}`,
		`AKIA[0-9A-Z]{16}`,
		`(?:ASIA|AGPA|AIDA|AROA|AIPA|ANPA|ANVA|ABIA|ACCA)[0-9A-Z]{16}`,
		`AIza[A-Za-z0-9_-]{35}`,
		`ya29\.[A-Za-z0-9_-]{20,}`,
		`do[opr]_v1_[a-f0-9]{64}`,
		`(?:sk|pk|rk)_(?:live|test)_[A-Za-z0-9]{16,}`,
		`sq0[a-z]{3}-[A-Za-z0-9_-]{22,43}`,
		`gh[pousr]_[A-Za-z0-9]{36}`,
		`github_pat_[A-Za-z0-9_]{20,}`,
		`gl(?:pat|ptt|rt|soat)-[A-Za-z0-9_-]{20}`,
		`glsa_[A-Za-z0-9]{32}_[A-Fa-f0-9]{8}`,
		`xox[abeoprs]-[A-Za-z0-9-]{10,}`,
		`xapp-[0-9]-[A-Za-z0-9-]{10,}`,
		`SK[0-9a-fA-F]{32}`,
		`SG\.[A-Za-z0-9_-]{22}\.[A-Za-z0-9_-]{43}`,
		`key-[0-9a-f]{32}`,
		`[0-9a-f]{32}-us[0-9]{1,2}`,
		`npm_[A-Za-z0-9]{36}`,
		`pypi-AgEI[A-Za-z0-9_-]{50,}`,
		`dapi[0-9a-f]{32}`,
		`dp\.pt\.[A-Za-z0-9]{40,}`,
		`PMAK-[a-f0-9]{24}-[a-f0-9]{34}`,
		`lin_api_[A-Za-z0-9]{40}`,
		`ntn_[A-Za-z0-9]{40,}`,
		`shp(?:at|ca|pa|ss)_[a-fA-F0-9]{32}`,
		`NR(?:AK|JS|II|MA|RA)-[A-Za-z0-9]{27}`,
		`eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{6,}`,
	}, "|"))
)

func looksLikeSecret(value string) bool {
	if strings.Contains(value, pemPrivateKeyMarker) {
		return true
	}
	length := utf8.RuneCountInString(value)
	if length < secretMinLength {
		return false
	}
	if isHighEntropySecret(value, length) {
		return true
	}
	return length <= knownSecretMaxScanLength && knownSecretPattern.MatchString(value)
}

func isHighEntropySecret(value string, length int) bool {
	if strings.Contains(value, " ") || looksLikePathOrURL(value) || uuidPattern.MatchString(value) {
		return false
	}

	// First-occurrence order keeps the entropy sum deterministic and in the
	// order Python's Counter adds it.
	counts := make(map[rune]int)
	var distinct []rune
	for _, r := range value {
		if counts[r] == 0 {
			distinct = append(distinct, r)
		}
		counts[r]++
	}

	var lower, upper, digit, symbol bool
	hexOnly := true
	for _, r := range distinct {
		switch {
		case strings.ContainsRune(secretRejectChars, r), isPythonSpace(r):
			return false
		case unicode.IsLower(r):
			lower = true
			hexOnly = hexOnly && r >= 'a' && r <= 'f'
		case unicode.IsUpper(r):
			upper = true
			hexOnly = hexOnly && r >= 'A' && r <= 'F'
		case unicode.IsDigit(r):
			digit = true
		default:
			symbol = true
			hexOnly = false
		}
	}
	if hexOnly {
		return false
	}
	classes := 0
	for _, present := range []bool{lower, upper, digit, symbol} {
		if present {
			classes++
		}
	}
	if classes < secretMinCharClasses {
		return false
	}

	entropy := 0.0
	for _, r := range distinct {
		p := float64(counts[r]) / float64(length)
		entropy -= p * math.Log2(p)
	}
	return entropy >= secretMinEntropyBits
}

func looksLikePathOrURL(value string) bool {
	if strings.Contains(value, "://") || strings.Contains(value, `\`) {
		return true
	}
	words := 0
	for _, segment := range strings.Split(value, "/") {
		if segment != "" && pathWordPattern.MatchString(segment) {
			words++
			if words >= 2 {
				return true
			}
		}
	}
	return false
}

// isPythonSpace is Python's str.isspace, which also counts the ASCII
// information separators U+001C..U+001F.
func isPythonSpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}
