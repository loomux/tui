package attach

import (
	"errors"
	"fmt"
	"strings"
)

// splitWords splits an attach command given as one shell line into its
// words, as a POSIX shell would: blanks separate words, and single
// quotes, double quotes and backslashes quote. It is for a command the
// server gave with no argv (a plugin's without one, and a local target's
// attach_command). The words are executed directly: there is never a
// shell.
//
// So a line that means more to a shell than its words is refused rather
// than run as something else: an expansion ($, `, ~, a glob, braces), a
// control operator or redirection (; & | < > ( )), a comment, a variable
// assignment before the command, a control character, or a quote left
// open.
func splitWords(line string) ([]string, error) {
	var (
		words  []string
		word   strings.Builder
		inWord bool
	)
	end := func() {
		if inWord {
			words = append(words, word.String())
			word.Reset()
			inWord = false
		}
	}
	needsShell := func(c rune) error {
		return fmt.Errorf("it has %q outside quotes, which only a shell can run", c)
	}
	rs := []rune(line)
	for _, c := range rs {
		if c != '\t' && (c < ' ' || c == 0x7f) {
			return nil, errors.New("it has a control character")
		}
	}
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch c {
		case ' ', '\t':
			end()
		case '\'':
			j := i + 1
			for j < len(rs) && rs[j] != '\'' {
				j++
			}
			if j == len(rs) {
				return nil, errors.New("a single quote is left open")
			}
			word.WriteString(string(rs[i+1 : j]))
			inWord = true
			i = j
		case '"':
			j := i + 1
			for ; j < len(rs) && rs[j] != '"'; j++ {
				switch rs[j] {
				case '$', '`':
					return nil, fmt.Errorf("it has %q inside double quotes, which only a shell can run", rs[j])
				case '\\':
					// Inside double quotes a backslash quotes only these;
					// before anything else it is itself.
					if j+1 < len(rs) && strings.ContainsRune("\"\\$`", rs[j+1]) {
						j++
					}
				}
				word.WriteRune(rs[j])
			}
			if j == len(rs) {
				return nil, errors.New("a double quote is left open")
			}
			inWord = true
			i = j
		case '\\':
			if i+1 == len(rs) {
				return nil, errors.New("it ends in a backslash")
			}
			i++
			word.WriteRune(rs[i])
			inWord = true
		case '$', '`', ';', '&', '|', '<', '>', '(', ')', '*', '?', '[', '{', '}':
			return nil, needsShell(c)
		case '#', '~':
			// A comment and a home directory, at the start of a word only.
			if !inWord {
				return nil, needsShell(c)
			}
			word.WriteRune(c)
		case '=':
			if len(words) == 0 {
				return nil, errors.New("it starts with a variable assignment, which only a shell can run")
			}
			word.WriteRune(c)
			inWord = true
		default:
			word.WriteRune(c)
			inWord = true
		}
	}
	end()
	if len(words) == 0 {
		return nil, errors.New("it is empty")
	}
	return words, nil
}
