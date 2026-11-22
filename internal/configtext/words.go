package configtext

import (
	"fmt"
	"strings"
)

// Words parses quoted words for the import formats. OpenSSH uses only double
// quotes for ordinary arguments, while Ansible INI uses shell-style quoting.
func Words(line string, singleQuotes bool) ([]string, error) {
	var words []string
	var word strings.Builder
	var quote byte
	active := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '\\' && quote != '\'':
			if i+1 == len(line) {
				if singleQuotes {
					return nil, fmt.Errorf("trailing backslash")
				}
				word.WriteByte(c)
				active = true
				continue
			}
			next := line[i+1]
			escape := quote == 0 && singleQuotes ||
				quote == '"' && (next == '"' || next == '\\') ||
				!singleQuotes && (next == '"' || next == '\\' || next == ' ' || next == '\t' || next == '#')
			if escape {
				i++
				c = next
			}
			word.WriteByte(c)
			active = true
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				word.WriteByte(c)
			}
		case c == '"' || singleQuotes && c == '\'':
			quote = c
			active = true
		case c == '#':
			i = len(line)
		case c == ' ' || c == '\t':
			if active {
				words = append(words, word.String())
				word.Reset()
				active = false
			}
		default:
			word.WriteByte(c)
			active = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quoted argument")
	}
	if active {
		words = append(words, word.String())
	}
	return words, nil
}
