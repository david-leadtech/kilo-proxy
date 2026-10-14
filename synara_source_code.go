package main

import "strings"

// Keep byte offsets while masking comments, literals and nested template
// expressions. Synara adapter sections contain regexes and templates with
// embedded strings; neither may advertise an executable source contract.
func synaraContractCode(source string) string {
	code := []byte(source)
	for i := 0; i < len(source); {
		end := synaraSourceLiteralEnd(source, i)
		if end > i {
			for j := i; j < end; j++ {
				if code[j] != '\n' && code[j] != '\r' {
					code[j] = ' '
				}
			}
			i = end
			continue
		}
		i++
	}
	return string(code)
}

func synaraSourceLiteralEnd(source string, i int) int {
	return synaraSourceLiteralEndDepth(source, i, 0)
}

func synaraSourceLiteralEndDepth(source string, i, depth int) int {
	if depth > 64 {
		return len(source)
	}
	ch := source[i]
	if ch == '\'' || ch == '"' {
		return synaraSourceQuotedEnd(source, i, ch)
	}
	if ch == '`' {
		return synaraSourceTemplateEnd(source, i, depth+1)
	}
	if ch != '/' || i+1 >= len(source) {
		return i
	}
	if source[i+1] == '/' {
		end := strings.IndexByte(source[i:], '\n')
		if end < 0 {
			return len(source)
		}
		return i + end
	}
	if source[i+1] == '*' {
		end := strings.Index(source[i+2:], "*/")
		if end < 0 {
			return len(source)
		}
		return i + end + 4
	}
	if !synaraSourceRegexStart(source, i) {
		return i
	}
	class := false
	for j := i + 1; j < len(source); j++ {
		switch source[j] {
		case '\\':
			j++
		case '[':
			class = true
		case ']':
			class = false
		case '/':
			if !class {
				return j + 1
			}
		case '\n', '\r':
			return len(source)
		}
	}
	return len(source)
}

func synaraSourceQuotedEnd(source string, i int, quote byte) int {
	for j := i + 1; j < len(source); j++ {
		if source[j] == '\\' {
			j++
		} else if source[j] == quote {
			return j + 1
		}
	}
	return len(source)
}

func synaraSourceTemplateEnd(source string, i, depth int) int {
	if depth > 64 {
		return len(source)
	}
	for j := i + 1; j < len(source); j++ {
		switch source[j] {
		case '\\':
			j++
		case '`':
			return j + 1
		case '$':
			if j+1 < len(source) && source[j+1] == '{' {
				j = synaraSourceTemplateExpressionEnd(source, j+2, depth+1) - 1
			}
		}
	}
	return len(source)
}

func synaraSourceTemplateExpressionEnd(source string, i, nesting int) int {
	if nesting > 64 {
		return len(source)
	}
	depth := 1
	for j := i; j < len(source); {
		end := synaraSourceLiteralEndDepth(source, j, nesting+1)
		if end > j {
			j = end
			continue
		}
		if source[j] == '{' {
			depth++
		} else if source[j] == '}' {
			depth--
			if depth == 0 {
				return j + 1
			}
		}
		j++
	}
	return len(source)
}

func synaraSourceRegexStart(source string, offset int) bool {
	i := offset - 1
	for i >= 0 && strings.ContainsRune(" \t\r\n", rune(source[i])) {
		i--
	}
	if i < 0 || strings.ContainsRune("=(:,[!&|?;{>", rune(source[i])) {
		return true
	}
	end := i + 1
	for i >= 0 && (source[i] >= 'a' && source[i] <= 'z' || source[i] >= 'A' && source[i] <= 'Z') {
		i--
	}
	switch source[i+1 : end] {
	case "return", "throw", "yield", "case", "typeof", "void", "delete":
		return true
	}
	return false
}
