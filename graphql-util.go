// Base64 helpers (the graphql cursors and global ids) and the GraphQL
// endpoint's parse-error recovery, shared across the graphql files.
package main

import (
	"encoding/base64"
)

func base64Encode(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func base64DecodeString(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}

// parseErrorBody rides back from a GraphQL parse failure.
var parseErrorBody []byte

// recoverParseError turns a gqlParser panic into a {"errors":[...]} body.
func recoverParseError() {
	if r := recover(); r != nil {
		if err, ok := r.(error); ok {
			parseErrorBody = []byte(`{"errors":[{"message":` + string(appendJSONString(nil, err.Error())) + `}]}`)
			return
		}
		parseErrorBody = []byte(`{"errors":[{"message":"Syntax Error"}]}`)
	}
}
