package db

import "strings"

type SelectName struct {
	Text   string
	Quoted bool
}

type SelectProjection struct {
	Qualifier SelectName
	Name      SelectName
	Star      bool
}

type SelectSource struct {
	Schema      SelectName
	Table       SelectName
	Alias       SelectName
	Projections []SelectProjection
}

type selectToken struct {
	name  SelectName
	other string
}

func (t selectToken) keyword(word string) bool {
	return !t.name.Quoted && strings.EqualFold(t.name.Text, word)
}
func (t selectToken) identifier() bool { return t.name.Text != "" }

// ParseSelectSource deliberately accepts only direct projections from one named table.
// Database metadata must still prove column ownership, the table kind, and a complete primary key.
func ParseSelectSource(statement string) (SelectSource, bool) {
	tokens, ok := selectTokens(statement)
	var source SelectSource
	if !ok || len(tokens) < 4 || !tokens[0].keyword("SELECT") {
		return source, false
	}
	from := 1
	for from < len(tokens) && !tokens[from].keyword("FROM") {
		from++
	}
	if from == len(tokens) || from == 1 {
		return source, false
	}
	for start := 1; start < from; {
		end := start
		for end < from && tokens[end].other != "," {
			end++
		}
		part := tokens[start:end]
		if len(part) == 0 {
			return source, false
		}
		for _, word := range []string{"ALL", "DISTINCT", "NOT", "NULL", "TRUE", "FALSE", "CURRENT_DATE", "CURRENT_TIME", "CURRENT_TIMESTAMP", "CURRENT_USER", "CURRENT_ROLE", "SESSION_USER", "SYSTEM_USER", "LOCALTIME", "LOCALTIMESTAMP", "USER"} {
			if part[0].keyword(word) {
				return source, false
			}
		}
		var projection SelectProjection
		i := 0
		if part[0].other == "*" {
			projection.Star = true
			i = 1
		} else if part[0].identifier() {
			projection.Name = part[0].name
			i = 1
			if len(part) > 2 && part[1].other == "." {
				projection.Qualifier = part[0].name
				if part[2].other == "*" {
					projection.Name = SelectName{}
					projection.Star = true
				} else if part[2].identifier() {
					projection.Name = part[2].name
				} else {
					return source, false
				}
				i = 3
			}
		} else {
			return source, false
		}
		if i < len(part) && part[i].keyword("AS") {
			i++
			if i == len(part) {
				return source, false
			}
		}
		if i < len(part) && (part[i].keyword("ISNULL") || part[i].keyword("NOTNULL")) {
			return source, false
		}
		if i < len(part) && part[i].identifier() && !projection.Star {
			i++
		}
		if i != len(part) {
			return source, false
		}
		source.Projections = append(source.Projections, projection)
		if end == from {
			break
		}
		start = end + 1
		if start == from {
			return source, false
		}
	}
	i := from + 1
	if i >= len(tokens) || !tokens[i].identifier() {
		return source, false
	}
	source.Table = tokens[i].name
	i++
	if i < len(tokens) && tokens[i].other == "." {
		if i+1 >= len(tokens) || !tokens[i+1].identifier() {
			return source, false
		}
		source.Schema = source.Table
		source.Table = tokens[i+1].name
		i += 2
	}
	if i < len(tokens) && tokens[i].keyword("AS") {
		i++
		if i >= len(tokens) || !tokens[i].identifier() {
			return source, false
		}
		source.Alias = tokens[i].name
		i++
	} else if i < len(tokens) && tokens[i].identifier() && !selectClause(tokens[i]) {
		source.Alias = tokens[i].name
		i++
	}
	if i < len(tokens) && !selectClause(tokens[i]) {
		return source, false
	}
	for ; i < len(tokens); i++ {
		if tokens[i].other == ";" {
			return source, false
		}
		for _, word := range []string{"SELECT", "FROM", "JOIN", "UNION", "INTERSECT", "EXCEPT", "WITH", "INTO", "RETURNING", "INSERT", "UPDATE", "DELETE", "FOR", "TABLE", "TABLESAMPLE", "WINDOW", "HAVING", "GROUP", "DISTINCT", "LATERAL"} {
			if tokens[i].keyword(word) {
				return source, false
			}
		}
	}
	return source, true
}

func selectClause(token selectToken) bool {
	return token.keyword("WHERE") || token.keyword("ORDER") || token.keyword("LIMIT") || token.keyword("OFFSET")
}

func selectTokens(statement string) ([]selectToken, bool) {
	tokens := []selectToken{}
	for i := 0; i < len(statement); {
		c := statement[i]
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\f' {
			i++
			continue
		}
		if strings.HasPrefix(statement[i:], "--") {
			for i < len(statement) && statement[i] != '\n' {
				i++
			}
			continue
		}
		if strings.HasPrefix(statement[i:], "/*") {
			i += 2
			depth := 1
			for i < len(statement) && depth > 0 {
				if strings.HasPrefix(statement[i:], "/*") {
					// SQLite does not nest comments; reject lexical differences between providers.
					return nil, false
				} else if strings.HasPrefix(statement[i:], "*/") {
					depth--
					i += 2
				} else {
					i++
				}
			}
			if depth != 0 {
				return nil, false
			}
			continue
		}
		if c == '\'' || c == '"' || c == '`' || c == '[' {
			end := c
			if c == '[' {
				end = ']'
			}
			i++
			var content strings.Builder
			closed := false
			for i < len(statement) {
				if statement[i] == '\\' {
					return nil, false
				}
				if statement[i] == end {
					if i+1 < len(statement) && statement[i+1] == end {
						content.WriteByte(end)
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				content.WriteByte(statement[i])
				i++
			}
			if !closed {
				return nil, false
			}
			if c == '\'' {
				tokens = append(tokens, selectToken{other: "literal"})
			} else {
				tokens = append(tokens, selectToken{name: SelectName{Text: content.String(), Quoted: true}})
			}
			continue
		}
		if c == '$' {
			end := i + 1
			for end < len(statement) && (statement[end] == '_' || statement[end] >= 'a' && statement[end] <= 'z' || statement[end] >= 'A' && statement[end] <= 'Z' || end > i+1 && statement[end] >= '0' && statement[end] <= '9') {
				end++
			}
			if end < len(statement) && statement[end] == '$' {
				delimiter := statement[i : end+1]
				close := strings.Index(statement[end+1:], delimiter)
				if close < 0 {
					return nil, false
				}
				i = end + 1 + close + len(delimiter)
				tokens = append(tokens, selectToken{other: "literal"})
				continue
			}
		}
		if c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 128 {
			start := i
			i++
			for i < len(statement) && (statement[i] == '_' || statement[i] == '$' || statement[i] >= 'a' && statement[i] <= 'z' || statement[i] >= 'A' && statement[i] <= 'Z' || statement[i] >= '0' && statement[i] <= '9' || statement[i] >= 128) {
				i++
			}
			tokens = append(tokens, selectToken{name: SelectName{Text: statement[start:i]}})
			continue
		}
		tokens = append(tokens, selectToken{other: string(c)})
		i++
	}
	if len(tokens) > 0 && tokens[len(tokens)-1].other == ";" {
		tokens = tokens[:len(tokens)-1]
	}
	return tokens, true
}
