package sqlite

/*
#include <stdlib.h>
extern int sqlite3_complete(const char *sql);
*/
import "C"

import (
	"errors"
	"strings"
	"unsafe"
)

// SQLite's parser recognizes trigger bodies as one statement despite their internal semicolons.
func singleStatement(sql string) (string, error) {
	if strings.ContainsRune(sql, '\x00') {
		return "", errors.New("SQL statements cannot contain NUL characters")
	}
	start := skipTrivia(sql, 0)
	if start == len(sql) {
		return "", errors.New("Enter one SQL statement to run")
	}
	sql = sql[start:]
	for index := 0; index < len(sql); index++ {
		switch sql[index] {
		case '\'', '"', '`', '[':
			end := sql[index]
			if end == '[' {
				end = ']'
			}
			for index++; index < len(sql); index++ {
				if sql[index] == end {
					if end != ']' && index+1 < len(sql) && sql[index+1] == end {
						index++
						continue
					}
					break
				}
			}
		case '-':
			if index+1 < len(sql) && sql[index+1] == '-' {
				for index < len(sql) && sql[index] != '\n' {
					index++
				}
			}
		case '/':
			if index+1 < len(sql) && sql[index+1] == '*' {
				end := strings.Index(sql[index+2:], "*/")
				if end < 0 {
					return sql, nil
				}
				index += end + 3
			}
		case ';':
			prefix := C.CString(sql[:index+1])
			complete := C.sqlite3_complete(prefix) != 0
			C.free(unsafe.Pointer(prefix))
			if complete {
				if skipTrivia(sql, index+1) != len(sql) {
					return "", errors.New("Run one SQL statement at a time")
				}
				return sql[:index+1], nil
			}
		}
	}
	return sql, nil
}

func skipTrivia(sql string, index int) int {
	for index < len(sql) {
		switch sql[index] {
		case ' ', '\t', '\r', '\n', '\f', ';':
			index++
		case '-':
			if index+1 >= len(sql) || sql[index+1] != '-' {
				return index
			}
			for index < len(sql) && sql[index] != '\n' {
				index++
			}
		case '/':
			if index+1 >= len(sql) || sql[index+1] != '*' {
				return index
			}
			end := strings.Index(sql[index+2:], "*/")
			if end < 0 {
				return len(sql)
			}
			index += end + 4
		default:
			return index
		}
	}
	return index
}
