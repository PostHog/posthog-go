package posthogmcp

import (
	"errors"
	"net/netip"
	"regexp"
	"strings"
	"unicode/utf8"
)

// The URL sanitizer is a port of posthog-python's, and its output is only the
// same when a URL splits, parses and re-serializes the way Python's
// urllib.parse (3.14) does it. net/url differs in all three: it rejects what
// Python accepts, sorts query keys, and encodes with different safe sets.
// These helpers reproduce the parts of urllib.parse the sanitizer calls.

var errInvalidURL = errors.New("invalid URL")

var ipvFuturePattern = regexp.MustCompile(`^[vV][a-fA-F0-9]+\..+$`)

// Characters NFKC normalizes into one of `/?#@:`; urlsplit rejects a netloc
// that holds one (`user：pass＠host` would otherwise hide its userinfo).
const nfkcURLDelimiters = "⁇⁈⁉℀℁℅℆⩴︓︖﹕﹖﹟﹫＃／：？＠"

var usesNetloc = map[string]bool{
	"": true, "ftp": true, "http": true, "gopher": true, "nntp": true, "telnet": true,
	"imap": true, "wais": true, "file": true, "mms": true, "https": true, "shttp": true,
	"snews": true, "prospero": true, "rtsp": true, "rtsps": true, "rtspu": true,
	"rsync": true, "svn": true, "svn+ssh": true, "sftp": true, "nfs": true, "git": true,
	"git+ssh": true, "ws": true, "wss": true, "itms-services": true,
}

type splitURL struct {
	scheme, netloc, path, query, fragment string
}

func urlsplit(value string) (splitURL, error) {
	var result splitURL
	if i := strings.IndexByte(value, ':'); i > 0 && isASCIILetter(value[0]) && isSchemeText(value[:i]) {
		result.scheme = strings.ToLower(value[:i])
		value = value[i+1:]
	}
	if strings.HasPrefix(value, "//") {
		end := len(value)
		if i := strings.IndexAny(value[2:], "/?#"); i >= 0 {
			end = 2 + i
		}
		result.netloc, value = value[2:end], value[end:]
		if err := checkNetloc(result.netloc); err != nil {
			return splitURL{}, err
		}
	}
	value, result.fragment, _ = strings.Cut(value, "#")
	result.path, result.query, _ = strings.Cut(value, "?")
	return result, nil
}

func isASCIILetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isSchemeText(value string) bool {
	for i := 0; i < len(value); i++ {
		c := value[i]
		if !isASCIILetter(c) && !(c >= '0' && c <= '9') && c != '+' && c != '-' && c != '.' {
			return false
		}
	}
	return true
}

func checkNetloc(netloc string) error {
	open, closed := strings.Contains(netloc, "["), strings.Contains(netloc, "]")
	if open != closed {
		return errInvalidURL
	}
	if open {
		if err := checkBracketedNetloc(netloc); err != nil {
			return err
		}
	}
	if strings.ContainsAny(netloc, nfkcURLDelimiters) {
		return errInvalidURL
	}
	return nil
}

func checkBracketedNetloc(netloc string) error {
	hostAndPort := netloc[strings.LastIndexByte(netloc, '@')+1:]
	var host string
	if before, bracketed, found := strings.Cut(hostAndPort, "["); found {
		if before != "" {
			return errInvalidURL
		}
		var port string
		host, port, _ = strings.Cut(bracketed, "]")
		if port != "" && !strings.HasPrefix(port, ":") {
			return errInvalidURL
		}
	} else {
		host, _, _ = strings.Cut(hostAndPort, ":")
	}
	if strings.HasPrefix(host, "v") || strings.HasPrefix(host, "V") {
		if !ipvFuturePattern.MatchString(host) {
			return errInvalidURL
		}
		return nil
	}
	address, err := netip.ParseAddr(host)
	if err != nil || address.Is4() {
		return errInvalidURL
	}
	return nil
}

func urlunsplit(u splitURL) string {
	var b strings.Builder
	if u.scheme != "" {
		b.WriteString(u.scheme)
		b.WriteByte(':')
	}
	switch {
	case u.netloc != "" || (u.scheme != "" && usesNetloc[u.scheme] && (u.path == "" || u.path[0] == '/')):
		b.WriteString("//")
		b.WriteString(u.netloc)
		if u.path != "" && u.path[0] != '/' {
			b.WriteByte('/')
		}
	case strings.HasPrefix(u.path, "//"):
		b.WriteString("//")
	}
	b.WriteString(u.path)
	if u.query != "" {
		b.WriteByte('?')
		b.WriteString(u.query)
	}
	if u.fragment != "" {
		b.WriteByte('#')
		b.WriteString(u.fragment)
	}
	return b.String()
}

type urlField struct {
	key, value string
}

// parseQSL is parse_qsl(text, keep_blank_values=True, max_num_fields=maxFields).
func parseQSL(text string, maxFields int) ([]urlField, error) {
	if text == "" {
		return nil, nil
	}
	if 1+strings.Count(text, "&") > maxFields {
		return nil, errInvalidURL
	}
	var fields []urlField
	for _, pair := range strings.Split(text, "&") {
		if pair == "" {
			continue
		}
		key, value, _ := strings.Cut(pair, "=")
		fields = append(fields, urlField{key: unquotePlus(key), value: unquotePlus(value)})
	}
	return fields, nil
}

func urlencode(fields []urlField) string {
	parts := make([]string, len(fields))
	for i, field := range fields {
		parts[i] = quotePlus(field.key) + "=" + quotePlus(field.value)
	}
	return strings.Join(parts, "&")
}

func quotePlus(value string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case isASCIILetter(c) || (c >= '0' && c <= '9') || strings.IndexByte("_.-~", c) >= 0:
			b.WriteByte(c)
		case c == ' ':
			b.WriteByte('+')
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0xf])
		}
	}
	return b.String()
}

// unquotePlus decodes `+` and %XX escapes the way Python's unquote_plus does:
// malformed escapes stay literal, and decoded bytes that are not UTF-8 become
// U+FFFD, one per maximal invalid subsequence.
func unquotePlus(value string) string {
	value = strings.ReplaceAll(value, "+", " ")
	if !strings.Contains(value, "%") {
		return value
	}
	var b strings.Builder
	for len(value) > 0 {
		next := strings.IndexFunc(value, func(r rune) bool { return r >= utf8.RuneSelf })
		if next == 0 {
			_, size := utf8.DecodeRuneInString(value)
			b.WriteString(value[:size])
			value = value[size:]
			continue
		}
		if next < 0 {
			next = len(value)
		}
		writeUTF8Replacing(&b, percentDecode(value[:next]))
		value = value[next:]
	}
	return b.String()
}

func percentDecode(value string) []byte {
	decoded := make([]byte, 0, len(value))
	for i := 0; i < len(value); i++ {
		if value[i] == '%' && i+2 < len(value) && isHexDigit(value[i+1]) && isHexDigit(value[i+2]) {
			decoded = append(decoded, unhex(value[i+1])<<4|unhex(value[i+2]))
			i += 2
			continue
		}
		decoded = append(decoded, value[i])
	}
	return decoded
}

func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func unhex(c byte) byte {
	switch {
	case c >= 'a':
		return c - 'a' + 10
	case c >= 'A':
		return c - 'A' + 10
	default:
		return c - '0'
	}
}

func writeUTF8Replacing(b *strings.Builder, data []byte) {
	for len(data) > 0 {
		r, size := utf8.DecodeRune(data)
		if r != utf8.RuneError || size > 1 {
			b.Write(data[:size])
			data = data[size:]
			continue
		}
		b.WriteRune(utf8.RuneError)
		data = data[invalidUTF8PrefixLength(data):]
	}
}

// invalidUTF8PrefixLength is the length of the maximal subpart of an
// ill-formed sequence (Unicode 3.9, Table 3-7), which Python replaces whole.
func invalidUTF8PrefixLength(data []byte) int {
	lead := data[0]
	var need int
	low, high := byte(0x80), byte(0xBF)
	switch {
	case lead >= 0xC2 && lead <= 0xDF:
		need = 1
	case lead == 0xE0:
		need, low = 2, 0xA0
	case lead == 0xED:
		need, high = 2, 0x9F
	case lead >= 0xE1 && lead <= 0xEF:
		need = 2
	case lead == 0xF0:
		need, low = 3, 0x90
	case lead == 0xF4:
		need, high = 3, 0x8F
	case lead >= 0xF1 && lead <= 0xF3:
		need = 3
	default:
		return 1
	}
	length := 1
	for length <= need && length < len(data) {
		if data[length] < low || data[length] > high {
			break
		}
		low, high = 0x80, 0xBF
		length++
	}
	return length
}
