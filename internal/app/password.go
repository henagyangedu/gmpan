package app

import (
	"strings"
	"unicode/utf8"
)

const PasswordRequirement = "密码至少 8 位，需同时包含字母和数字，最多 72 字节，不能包含换行或空字符"

// ValidPassword applies when setting a password, never when checking an old login.
func ValidPassword(password string) bool {
	if !utf8.ValidString(password) || utf8.RuneCountInString(password) < 8 || len(password) > 72 || strings.ContainsAny(password, "\x00\r\n") {
		return false
	}
	letter, digit := false, false
	for _, ch := range password {
		letter = letter || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z'
		digit = digit || ch >= '0' && ch <= '9'
	}
	return letter && digit
}
