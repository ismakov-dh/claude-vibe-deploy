package shell

import (
	"errors"
	"strings"
)

// Split breaks a command line into words the way sh does for quoting —
// '…' literal, "…" with \" \\ \$ \` escapes, \ outside quotes — and does
// nothing else: no variables, no command substitution, no globs.
func Split(line string) ([]string, error) {
	var words []string
	var cur strings.Builder
	inWord := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			if inWord {
				words, inWord = append(words, cur.String()), false
				cur.Reset()
			}
			continue
		case c == '\'':
			j := strings.IndexByte(line[i+1:], '\'')
			if j < 0 {
				return nil, errors.New("unterminated ' quote")
			}
			cur.WriteString(line[i+1 : i+1+j])
			i += j + 1
		case c == '"':
			i++
			for ; i < len(line) && line[i] != '"'; i++ {
				if line[i] == '\\' && i+1 < len(line) && strings.IndexByte("\"\\$`", line[i+1]) >= 0 {
					i++
				}
				cur.WriteByte(line[i])
			}
			if i >= len(line) {
				return nil, errors.New(`unterminated " quote`)
			}
		case c == '\\':
			if i+1 == len(line) {
				return nil, errors.New("trailing backslash")
			}
			i++
			cur.WriteByte(line[i])
		default:
			cur.WriteByte(c)
		}
		inWord = true
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words, nil
}

// Quote makes s one literal word for sh.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
