package store

import (
	"fmt"
	"html"
	"strings"
)

// ErrNotAnEmailAddress is returned when a value offered as an email contact is not one. It is a
// distinct error because the caller usually wants to keep going with the other contacts it
// found rather than abandon the whole page.
var ErrNotAnEmailAddress = fmt.Errorf("value is not a usable email address")

// NormalizeContactValue cleans a value extracted from a web page before anything stores it.
//
// Two real defects motivated this. An address arrived as "u003emgmt@stickermule.com" — the
// leading `>` of a tag had been JSON-escaped to `>`, the backslash lost somewhere, and the
// remainder concatenated onto the local part. And a phone number, "+254123456789", was stored
// under kind='email'. Both were caught downstream, which is the right place for a last line of
// defence but the wrong place for the only one: a stored value that was never an address
// pollutes every count and every listing that reads it.
func NormalizeContactValue(kind, raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", ErrNotAnEmailAddress
	}
	// Unescape HTML entities and the JSON unicode escapes that survive a mangled extraction,
	// repeatedly, because a doubly-escaped value is common in scraped markup.
	for i := 0; i < 3; i++ {
		decoded := html.UnescapeString(unescapeJSONUnicode(value))
		if decoded == value {
			break
		}
		value = decoded
	}
	value = strings.TrimSpace(value)
	// Strip markup fragments that survived extraction, and the punctuation a sentence leaves on
	// an address quoted mid-text.
	value = strings.Trim(value, "<>\"'()[]{},;: \t\r\n")
	if kind != "email" {
		if value == "" {
			return "", ErrNotAnEmailAddress
		}
		return value, nil
	}
	value = strings.ToLower(value)
	value = strings.TrimPrefix(value, "mailto:")
	if err := validateEmailAddress(value); err != nil {
		return "", err
	}
	return value, nil
}

// validateEmailAddress is deliberately strict about shape. It is not trying to decide whether a
// mailbox exists; it is trying to ensure that what is stored as an email address is one.
func validateEmailAddress(value string) error {
	at := strings.LastIndex(value, "@")
	if at <= 0 || at == len(value)-1 {
		return ErrNotAnEmailAddress
	}
	local, host := value[:at], value[at+1:]
	if strings.Contains(local, "@") || !strings.Contains(host, ".") {
		return ErrNotAnEmailAddress
	}
	if strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") || strings.Contains(host, "..") {
		return ErrNotAnEmailAddress
	}
	// A phone number is the specific wrong thing that reached the email path, so refuse anything
	// whose local part is only digits and dialling punctuation.
	if isPhoneLike(local) {
		return ErrNotAnEmailAddress
	}
	for _, r := range value {
		if r <= ' ' || r == '<' || r == '>' || r == '"' || r == ',' || r == ';' || r == '\\' {
			return ErrNotAnEmailAddress
		}
	}
	// A bare "+254123456789" never reaches here because it has no @, but a value like
	// "+254123456789@" or a stray label does; the checks above cover those.
	if len(value) > 254 || len(local) > 64 {
		return ErrNotAnEmailAddress
	}
	return nil
}

func isPhoneLike(local string) bool {
	digits := 0
	for _, r := range local {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == '+' || r == '-' || r == ' ' || r == '(' || r == ')' || r == '.':
		default:
			return false
		}
	}
	return digits >= 6
}

// unescapeJSONUnicode turns \uXXXX sequences into their characters, including the mangled form
// where the backslash has been lost and only "uXXXX" remains at the start of a value — which is
// exactly how "u003emgmt@…" came to be stored.
func unescapeJSONUnicode(value string) string {
	if !strings.Contains(value, "\\u") && !strings.HasPrefix(value, "u00") {
		return value
	}
	var b strings.Builder
	for i := 0; i < len(value); {
		if value[i] == '\\' && i+5 < len(value) && value[i+1] == 'u' {
			if r, ok := decodeHex4(value[i+2 : i+6]); ok {
				b.WriteRune(r)
				i += 6
				continue
			}
		}
		// The lost-backslash form only makes sense at the very start; treating "u0041" in the
		// middle of a real address as an escape would corrupt valid values.
		if i == 0 && len(value) >= 5 && value[0] == 'u' {
			if r, ok := decodeHex4(value[1:5]); ok {
				b.WriteRune(r)
				i += 5
				continue
			}
		}
		b.WriteByte(value[i])
		i++
	}
	return b.String()
}

func decodeHex4(s string) (rune, bool) {
	if len(s) != 4 {
		return 0, false
	}
	var value rune
	for _, c := range s {
		var digit rune
		switch {
		case c >= '0' && c <= '9':
			digit = c - '0'
		case c >= 'a' && c <= 'f':
			digit = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			digit = c - 'A' + 10
		default:
			return 0, false
		}
		value = value*16 + digit
	}
	return value, true
}
