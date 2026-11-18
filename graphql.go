// GraphQL v4: POST /api/graphql, the documents gh and github-mcp-server send
// (graphql.ts): a hand-rolled parser and executor over the one store, GitHub's
// error discipline, and operation batching.
//
// The executor resolves a document into an ordered field tree (gqlField),
// then serializes it once. Every resolver answers (value, ok): ok=false is a
// resolver level "null" (a missing repo, an unreadable object); the MISSING
// sentinel is a schema-level unknown field (an errors entry).
package main

import (
	"encoding/base64"
	"regexp"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------- the parser

type gqlTokenKind int

const (
	gqlName gqlTokenKind = iota
	gqlIntT
	gqlFloatT
	gqlStringT
	gqlPunct
	gqlEOF
)

type gqlToken struct {
	kind  gqlTokenKind
	value string
}

func gqlTokenize(source string) []gqlToken {
	var tokens []gqlToken
	index := 0
	for index < len(source) {
		ch := source[index]
		if ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n' || ch == ',' {
			index++
			continue
		}
		if ch == '#' {
			for index < len(source) && source[index] != '\n' {
				index++
			}
			continue
		}
		if ch == '"' && index+2 < len(source) && source[index+1] == '"' && source[index+2] == '"' {
			end := strings.Index(source[index+3:], `"""`)
			var value string
			var next int
			if end < 0 {
				value = source[index+3:]
				next = len(source)
			} else {
				value = source[index+3 : index+3+end]
				next = index + 3 + end + 3
			}
			tokens = append(tokens, gqlToken{kind: gqlStringT, value: value})
			index = next
			continue
		}
		if ch == '"' {
			var text []byte
			index++
			for index < len(source) && source[index] != '"' {
				if source[index] == '\\' && index+1 < len(source) {
					escape := source[index+1]
					switch escape {
					case 'n':
						text = append(text, '\n')
					case 't':
						text = append(text, '\t')
					case '"':
						text = append(text, '"')
					case '\\':
						text = append(text, '\\')
					default:
						text = append(text, escape)
					}
					index += 2
					continue
				}
				text = append(text, source[index])
				index++
			}
			index++
			tokens = append(tokens, gqlToken{kind: gqlStringT, value: string(text)})
			continue
		}
		if ch == '.' && index+2 < len(source) && source[index+1] == '.' && source[index+2] == '.' {
			tokens = append(tokens, gqlToken{kind: gqlPunct, value: "..."})
			index += 3
			continue
		}
		if strings.IndexByte("!$():=[]{}|@&", ch) >= 0 {
			tokens = append(tokens, gqlToken{kind: gqlPunct, value: string(ch)})
			index++
			continue
		}
		if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || ch == '_' {
			start := index
			for index < len(source) {
				c := source[index]
				if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' {
					index++
				} else {
					break
				}
			}
			tokens = append(tokens, gqlToken{kind: gqlName, value: source[start:index]})
			continue
		}
		if ch == '-' || (ch >= '0' && ch <= '9') {
			start := index
			for index < len(source) {
				c := source[index]
				if (c >= '0' && c <= '9') || c == '.' || c == 'e' || c == 'E' || c == '+' || c == '-' {
					index++
				} else {
					break
				}
			}
			text := source[start:index]
			kind := gqlIntT
			if strings.ContainsAny(text, ".eE") {
				kind = gqlFloatT
			}
			tokens = append(tokens, gqlToken{kind: kind, value: text})
			continue
		}
		index++
	}
	tokens = append(tokens, gqlToken{kind: gqlEOF, value: ""})
	return tokens
}

// ---------------------------------------------------------------- the AST

type gqlValueKind int

const (
	gqlVar gqlValueKind = iota
	gqlVInt
	gqlVFloat
	gqlVString
	gqlVEnum
	gqlVBool
	gqlVNull
	gqlVList
	gqlVObject
)

type gqlValueNode struct {
	kind    gqlValueKind
	str     string
	num     float64
	boolean bool
	items   []gqlValueNode
	fields  []gqlAstMember
}

type gqlArgument struct {
	name  string
	value gqlValueNode
}

// gqlAstMember is one key/value pair of an AST object value (the value is a node).
type gqlAstMember struct {
	key   string
	value gqlValueNode
}

type gqlSelection struct {
	kind          int // 0 field, 1 inlineFragment, 2 fragmentSpread
	alias         string
	hasAlias      bool
	name          string
	args          []gqlArgument
	selection     []gqlSelection
	typeCondition string
}

type gqlOperation struct {
	isMutation bool
	name       string
	hasName    bool
	selection  []gqlSelection
}

type gqlFragment struct {
	name          string
	typeCondition string
	selection     []gqlSelection
}

type gqlDocument struct {
	operations []gqlOperation
	fragments  map[string]gqlFragment
}

type gqlParseError struct{ msg string }

func (e *gqlParseError) Error() string { return e.msg }

func gqlFoundOf(t gqlToken) string {
	if t.value == "" {
		return "<EOF>"
	}
	return "'" + t.value + "'"
}

type gqlParser struct {
	tokens []gqlToken
	index  int
}

func (p *gqlParser) peek() gqlToken { return p.tokens[p.index] }

func (p *gqlParser) next() gqlToken {
	t := p.tokens[p.index]
	p.index++
	return t
}

func (p *gqlParser) name() string {
	t := p.next()
	if t.kind != gqlName {
		panic(&gqlParseError{"Syntax Error: expected a name, found " + gqlFoundOf(t)})
	}
	return t.value
}

func (p *gqlParser) expectPunct(value string) {
	t := p.next()
	if t.kind != gqlPunct || t.value != value {
		panic(&gqlParseError{"Syntax Error: expected '" + value + "', found " + gqlFoundOf(t)})
	}
}

func (p *gqlParser) skipPunct(value string) bool {
	t := p.peek()
	if t.kind == gqlPunct && t.value == value {
		p.index++
		return true
	}
	return false
}

func (p *gqlParser) document() gqlDocument {
	doc := gqlDocument{fragments: map[string]gqlFragment{}}
	for {
		t := p.peek()
		if t.kind == gqlEOF {
			break
		}
		if t.kind == gqlName && (t.value == "query" || t.value == "mutation") {
			doc.operations = append(doc.operations, p.operation())
		} else if t.kind == gqlName && t.value == "fragment" {
			fragment := p.fragment()
			doc.fragments[fragment.name] = fragment
		} else if t.kind == gqlPunct && t.value == "{" {
			doc.operations = append(doc.operations, gqlOperation{selection: p.selectionSet()})
		} else {
			panic(&gqlParseError{"Syntax Error: unexpected " + gqlFoundOf(t)})
		}
	}
	if len(doc.operations) == 0 {
		panic(&gqlParseError{"Syntax Error: the document holds no operation"})
	}
	return doc
}

func (p *gqlParser) operation() gqlOperation {
	t := p.next()
	op := gqlOperation{isMutation: t.value == "mutation"}
	if p.peek().kind == gqlName {
		op.name = p.name()
		op.hasName = true
	}
	if p.skipPunct("(") {
		for !p.skipPunct(")") {
			if p.peek().kind == gqlEOF {
				panic(&gqlParseError{"Syntax Error: unterminated variable definitions"})
			}
			p.next()
		}
	}
	p.skipDirectives()
	op.selection = p.selectionSet()
	return op
}

func (p *gqlParser) skipDirectives() {
	for p.skipPunct("@") {
		if p.peek().kind == gqlName {
			p.name()
		}
		if p.skipPunct("(") {
			for !p.skipPunct(")") {
				if p.peek().kind == gqlEOF {
					panic(&gqlParseError{"Syntax Error: unterminated directive arguments"})
				}
				p.next()
			}
		}
	}
}

func (p *gqlParser) fragment() gqlFragment {
	t := p.next()
	if t.kind != gqlName || t.value != "fragment" {
		panic(&gqlParseError{"Syntax Error: expected 'fragment', found " + gqlFoundOf(t)})
	}
	name := p.name()
	t2 := p.next()
	if t2.kind != gqlName || t2.value != "on" {
		panic(&gqlParseError{"Syntax Error: expected 'on', found " + gqlFoundOf(t2)})
	}
	typeCondition := p.name()
	return gqlFragment{name: name, typeCondition: typeCondition, selection: p.selectionSet()}
}

func (p *gqlParser) selectionSet() []gqlSelection {
	p.expectPunct("{")
	var selection []gqlSelection
	for !p.skipPunct("}") {
		if p.peek().kind == gqlEOF {
			panic(&gqlParseError{"Syntax Error: unterminated selection set"})
		}
		if p.peek().kind == gqlPunct && p.peek().value == "..." {
			p.next()
			if p.peek().kind == gqlName && p.peek().value == "on" {
				p.next()
				typeCondition := p.name()
				selection = append(selection, gqlSelection{kind: 1, typeCondition: typeCondition, selection: p.selectionSet()})
			} else {
				selection = append(selection, gqlSelection{kind: 2, name: p.name()})
			}
			continue
		}
		first := p.name()
		field := gqlSelection{kind: 0, name: first}
		if p.skipPunct(":") {
			field.alias = first
			field.hasAlias = true
			field.name = p.name()
		}
		if p.skipPunct("(") {
			for !p.skipPunct(")") {
				argName := p.name()
				p.expectPunct(":")
				field.args = append(field.args, gqlArgument{name: argName, value: p.value(false)})
			}
		}
		p.skipDirectives()
		if p.peek().kind == gqlPunct && p.peek().value == "{" {
			field.selection = p.selectionSet()
		}
		selection = append(selection, field)
	}
	return selection
}

func (p *gqlParser) value(constant bool) gqlValueNode {
	t := p.next()
	switch t.kind {
	case gqlIntT:
		n, _ := strconv.ParseFloat(t.value, 64)
		return gqlValueNode{kind: gqlVInt, num: n}
	case gqlFloatT:
		n, _ := strconv.ParseFloat(t.value, 64)
		return gqlValueNode{kind: gqlVFloat, num: n}
	case gqlStringT:
		return gqlValueNode{kind: gqlVString, str: t.value}
	case gqlName:
		switch t.value {
		case "true":
			return gqlValueNode{kind: gqlVBool, boolean: true}
		case "false":
			return gqlValueNode{kind: gqlVBool, boolean: false}
		case "null":
			return gqlValueNode{kind: gqlVNull}
		}
		if constant {
			panic(&gqlParseError{"Syntax Error: unexpected '" + t.value + "'"})
		}
		return gqlValueNode{kind: gqlVEnum, str: t.value}
	case gqlPunct:
		switch t.value {
		case "$":
			if constant {
				panic(&gqlParseError{"Syntax Error: a variable in a constant position"})
			}
			return gqlValueNode{kind: gqlVar, str: p.name()}
		case "[":
			items := []gqlValueNode{}
			for !p.skipPunct("]") {
				if p.peek().kind == gqlEOF {
					panic(&gqlParseError{"Syntax Error: unterminated list"})
				}
				items = append(items, p.value(constant))
			}
			return gqlValueNode{kind: gqlVList, items: items}
		case "{":
			var fields []gqlAstMember
			for !p.skipPunct("}") {
				name := p.name()
				p.expectPunct(":")
				fields = append(fields, gqlAstMember{key: name, value: p.value(constant)})
			}
			return gqlValueNode{kind: gqlVObject, fields: fields}
		}
	}
	panic(&gqlParseError{"Syntax Error: unexpected " + gqlFoundOf(t)})
}

// resolveValue evaluates a value node against variables, into the jsonValue model.
func resolveValue(node gqlValueNode, variables map[string]jsonValue) jsonValue {
	switch node.kind {
	case gqlVar:
		if v, ok := variables[node.str]; ok {
			return v
		}
		return jsonValue{kind: jsonNull}
	case gqlVInt, gqlVFloat:
		return jsonValue{kind: jsonNumber, num: node.num}
	case gqlVString, gqlVEnum:
		return jsonValue{kind: jsonString, str: node.str}
	case gqlVBool:
		return jsonValue{kind: jsonBool, boolean: node.boolean}
	case gqlVNull:
		return jsonValue{kind: jsonNull}
	case gqlVList:
		out := jsonValue{kind: jsonArray}
		for _, item := range node.items {
			out.items = append(out.items, resolveValue(item, variables))
		}
		return out
	case gqlVObject:
		out := jsonValue{kind: jsonObject}
		for _, f := range node.fields {
			out.members = append(out.members, jsonMember{key: f.key, value: resolveValue(f.value, variables)})
		}
		return out
	}
	return jsonValue{kind: jsonNull}
}

// ---------------------------------------------------------------- the executor

type gqlErrorEntry struct {
	message string
	path    []string
}

type gqlContext struct {
	forge     *forge
	variables map[string]jsonValue
	fragments map[string]gqlFragment
	errors    []gqlErrorEntry
}

// gqlMissing is the sentinel a missing field resolves to (an errors entry rides
// alongside in the executor).
type gqlMissingType struct{}

func (gqlMissingType) String() string { return "<missing>" }

var gqlMissing = &gqlMissingType{}

func globalId(typeName, id string) string {
	return base64.StdEncoding.EncodeToString([]byte("012:" + typeName + ":" + id))
}

var globalIDDecodeRe = regexp.MustCompile(`^012:([A-Za-z]+):(.+)$`)

func decodeGlobalId(id string) (string, string, bool) {
	text, err := base64.StdEncoding.DecodeString(id)
	if err != nil {
		return "", "", false
	}
	m := globalIDDecodeRe.FindSubmatch(text)
	if m == nil {
		return "", "", false
	}
	return string(m[1]), string(m[2]), true
}

func gqlArgOf(field gqlSelection, name string, variables map[string]jsonValue) (jsonValue, bool) {
	for _, arg := range field.args {
		if arg.name == name {
			return resolveValue(arg.value, variables), true
		}
	}
	return jsonValue{}, false
}

func gqlArgStr(field gqlSelection, name string, variables map[string]jsonValue) (string, bool) {
	v, ok := gqlArgOf(field, name, variables)
	if ok && v.kind == jsonString {
		return v.str, true
	}
	return "", false
}

func gqlArgNumber(field gqlSelection, name string, variables map[string]jsonValue) (float64, bool) {
	v, ok := gqlArgOf(field, name, variables)
	if ok && v.kind == jsonNumber {
		return v.num, true
	}
	return 0, false
}

func gqlRepoArg(f *forge, field gqlSelection, variables map[string]jsonValue) *repo {
	owner, hasOwner := gqlArgStr(field, "owner", variables)
	name, hasName := gqlArgStr(field, "name", variables)
	if !hasOwner || !hasName {
		return nil
	}
	r, ok := f.getRepo(owner, name)
	if !ok {
		return nil
	}
	return r
}

var gqlKeyIRe = regexp.MustCompile(`^(\d+)I(\d+)$`)
var gqlKeyPRe = regexp.MustCompile(`^(\d+)P(\d+)$`)

// gqlField is one resolved response field: its key and its finished JSON bytes.
type gqlField struct {
	key  string
	data []byte
}

// gqlNode is one resolved response object: its fields in order.
type gqlNode struct {
	fields []gqlField
}

func (n *gqlNode) emit(key string, data []byte) {
	n.fields = append(n.fields, gqlField{key: key, data: data})
}

func (n *gqlNode) emitNode(key string, child *gqlNode) {
	n.emit(key, child.bytes())
}

func (n *gqlNode) bytes() []byte {
	buf := make([]byte, 0, 64)
	buf = append(buf, '{')
	for i, f := range n.fields {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = appendJSONString(buf, f.key)
		buf = append(buf, ':')
		buf = append(buf, f.data...)
	}
	buf = append(buf, '}')
	return buf
}

func (n *gqlNode) emitValue(key string, v jsonValue) {
	o := newJSON()
	o.jv(v)
	n.emit(key, o.Bytes())
}

func gqlSerialize(v jsonValue) []byte {
	o := newJSON()
	o.jv(v)
	return o.Bytes()
}

// childSelection answers the named sub-selection of one field.
func childSelection(selection []gqlSelection, name string) []gqlSelection {
	for _, child := range selection {
		if child.kind == 0 && child.name == name {
			return child.selection
		}
	}
	return nil
}

func messageOfCommit(commitText string) string {
	blank := strings.Index(commitText, "\n\n")
	if blank < 0 {
		return ""
	}
	return commitText[blank+2:]
}

// currentRepo answers the repo an object belongs to, tracked loosely: the
// forge's first repo (graphql.ts's per-executor cache, in creation order).
func (f *forge) currentRepo() *repo {
	for _, key := range f.repoOrder {
		if r, ok := f.repos[key]; ok {
			return r
		}
	}
	return nil
}

// gqlExecute answers one selection set's resolved node against typeName/source.
func gqlExecute(ctx *gqlContext, typeName string, source any, selection []gqlSelection, path []string) *gqlNode {
	node := &gqlNode{}
	for _, sel := range selection {
		switch sel.kind {
		case 1: // inlineFragment
			if sel.typeCondition != "" && sel.typeCondition != typeName {
				ok := sel.typeCondition == "Node" && (typeName == "Repository" || typeName == "Issue" || typeName == "PullRequest")
				if !ok {
					continue
				}
			}
			inner := gqlExecute(ctx, typeName, source, sel.selection, path)
			node.fields = append(node.fields, inner.fields...)
			continue
		case 2: // fragmentSpread
			fragment, ok := ctx.fragments[sel.name]
			if !ok {
				ctx.errors = append(ctx.errors, gqlErrorEntry{message: `Fragment "` + sel.name + `" is not defined in the document.`})
				continue
			}
			if fragment.typeCondition != typeName {
				continue
			}
			inner := gqlExecute(ctx, typeName, source, fragment.selection, path)
			node.fields = append(node.fields, inner.fields...)
			continue
		}
		key := sel.name
		if sel.hasAlias {
			key = sel.alias
		}
		if sel.name == "__typename" {
			node.emit(key, appendJSONString(nil, typeName))
			continue
		}
		result := gqlResolveField(ctx, typeName, source, sel, path)
		if miss, isMissing := result.(*gqlMissingType); isMissing && miss != nil {
			entry := gqlErrorEntry{message: `Cannot query field "` + sel.name + `" on type "` + typeName + `".`}
			entry.path = append(append([]string{}, path...), key)
			ctx.errors = append(ctx.errors, entry)
			continue
		}
		if n, ok := result.(*gqlNode); ok {
			node.emitNode(key, n)
			continue
		}
		if b, ok := result.([]byte); ok {
			node.emit(key, b)
			continue
		}
		// A plain value the resolver already shaped.
		switch v := result.(type) {
		case nil:
			node.emit(key, []byte("null"))
		case string:
			node.emit(key, appendJSONString(nil, v))
		case int:
			node.emit(key, []byte(strconv.Itoa(v)))
		case bool:
			node.emit(key, []byte(strconv.FormatBool(v)))
		case jsonValue:
			node.emitValue(key, v)
		default:
			node.emit(key, []byte("null"))
		}
	}
	return node
}

// innerPath answers path plus this field's key.
func innerPath(path []string, field gqlSelection) []string {
	key := field.name
	if field.hasAlias {
		key = field.alias
	}
	return append(append([]string{}, path...), key)
}

// ---------------------------------------------------------------- introspection

type introspectTypeDef struct {
	name       string
	kind       string
	fields     map[string]string // field name -> type spelling
	fieldOrder []string
	enumValues []string
}

var introspectionTypes = map[string]*introspectTypeDef{
	"Query": {name: "Query", kind: "OBJECT", fields: map[string]string{
		"viewer": "User!", "repository": "Repository", "node": "Node", "__type": "__Type", "__schema": "__Schema!",
	}, fieldOrder: []string{"viewer", "repository", "node", "__type", "__schema"}},
	"Mutation": {name: "Mutation", kind: "OBJECT", fields: map[string]string{
		"createIssue": "CreateIssuePayload", "closeIssue": "CloseIssuePayload", "createPullRequest": "CreatePullRequestPayload",
		"mergePullRequest": "MergePullRequestPayload", "addComment": "AddCommentPayload",
	}, fieldOrder: []string{"createIssue", "closeIssue", "createPullRequest", "mergePullRequest", "addComment"}},
	"Repository": {name: "Repository", kind: "OBJECT", fields: map[string]string{
		"id": "ID!", "databaseId": "Int", "name": "String!", "nameWithOwner": "String!", "owner": "User!",
		"defaultBranchRef": "Ref", "viewerPermission": "RepositoryPermission", "hasIssuesEnabled": "Boolean!",
		"issue": "Issue", "issues": "IssueConnection!", "pullRequest": "PullRequest", "pullRequests": "PullRequestConnection!",
	}, fieldOrder: []string{"id", "databaseId", "name", "nameWithOwner", "owner", "defaultBranchRef", "viewerPermission", "hasIssuesEnabled", "issue", "issues", "pullRequest", "pullRequests"}},
	"Issue": {name: "Issue", kind: "OBJECT", fields: map[string]string{
		"id": "ID!", "number": "Int!", "title": "String!", "state": "IssueState!", "body": "String",
		"createdAt": "DateTime!", "updatedAt": "DateTime!", "closedAt": "DateTime", "author": "Actor",
		"bodyText": "String", "comments": "IssueCommentConnection!", "labels": "LabelConnection",
		"repository": "Repository!", "url": "URI!", "viewerCanUpdate": "Boolean!",
	}, fieldOrder: []string{"id", "number", "title", "state", "body", "createdAt", "updatedAt", "closedAt", "author", "bodyText", "comments", "labels", "repository", "url", "viewerCanUpdate"}},
	"IssueConnection":              {name: "IssueConnection", kind: "OBJECT", fields: map[string]string{"nodes": "[Issue]", "edges": "[IssueEdge]", "pageInfo": "PageInfo!", "totalCount": "Int!"}, fieldOrder: []string{"nodes", "edges", "pageInfo", "totalCount"}},
	"IssueEdge":                    {name: "IssueEdge", kind: "OBJECT", fields: map[string]string{"node": "Issue", "cursor": "String!"}, fieldOrder: []string{"node", "cursor"}},
	"PullRequest": {name: "PullRequest", kind: "OBJECT", fields: map[string]string{
		"id": "ID!", "number": "Int!", "title": "String!", "state": "PullRequestState!", "merged": "Boolean!",
		"headRefName": "String!", "baseRefName": "String!", "mergeable": "MergeableState!",
		"files": "PullRequestChangedFileConnection", "body": "String", "createdAt": "DateTime!", "updatedAt": "DateTime!",
		"mergedAt": "DateTime", "mergeCommit": "Commit", "mergeCommitSha": "GitObjectID", "author": "Actor", "url": "URI!",
	}, fieldOrder: []string{"id", "number", "title", "state", "merged", "headRefName", "baseRefName", "mergeable", "files", "body", "createdAt", "updatedAt", "mergedAt", "mergeCommit", "mergeCommitSha", "author", "url"}},
	"PullRequestConnection":                 {name: "PullRequestConnection", kind: "OBJECT", fields: map[string]string{"nodes": "[PullRequest]", "edges": "[PullRequestEdge]", "pageInfo": "PageInfo!", "totalCount": "Int!"}, fieldOrder: []string{"nodes", "edges", "pageInfo", "totalCount"}},
	"PullRequestEdge":                       {name: "PullRequestEdge", kind: "OBJECT", fields: map[string]string{"node": "PullRequest", "cursor": "String!"}, fieldOrder: []string{"node", "cursor"}},
	"PullRequestChangedFile":                {name: "PullRequestChangedFile", kind: "OBJECT", fields: map[string]string{"path": "String!", "additions": "Int!", "deletions": "Int!"}, fieldOrder: []string{"path", "additions", "deletions"}},
	"PullRequestChangedFileConnection":      {name: "PullRequestChangedFileConnection", kind: "OBJECT", fields: map[string]string{"nodes": "[PullRequestChangedFile]", "pageInfo": "PageInfo!", "totalCount": "Int!"}, fieldOrder: []string{"nodes", "pageInfo", "totalCount"}},
	"User":                                  {name: "User", kind: "OBJECT", fields: map[string]string{"login": "String!", "id": "ID!", "name": "String", "avatarUrl": "URI", "url": "URI!", "createdAt": "DateTime!"}, fieldOrder: []string{"login", "id", "name", "avatarUrl", "url", "createdAt"}},
	"Ref":                                   {name: "Ref", kind: "OBJECT", fields: map[string]string{"id": "ID!", "name": "String!", "target": "GitObject"}, fieldOrder: []string{"id", "name", "target"}},
	"Commit":                                {name: "Commit", kind: "OBJECT", fields: map[string]string{"id": "ID!", "oid": "GitObjectID!", "message": "String!", "committedDate": "DateTime!", "authoredDate": "DateTime!"}, fieldOrder: []string{"id", "oid", "message", "committedDate", "authoredDate"}},
	"Tag":                                   {name: "Tag", kind: "OBJECT", fields: map[string]string{"id": "ID!", "oid": "GitObjectID!", "message": "String!"}, fieldOrder: []string{"id", "oid", "message"}},
	"IssueComment":                          {name: "IssueComment", kind: "OBJECT", fields: map[string]string{"id": "ID!", "body": "String!", "createdAt": "DateTime!", "author": "Actor"}, fieldOrder: []string{"id", "body", "createdAt", "author"}},
	"PageInfo":                              {name: "PageInfo", kind: "OBJECT", fields: map[string]string{"hasNextPage": "Boolean!", "endCursor": "String", "startCursor": "String", "hasPreviousPage": "Boolean!"}, fieldOrder: []string{"hasNextPage", "endCursor", "startCursor", "hasPreviousPage"}},
	"Node":                                  {name: "Node", kind: "INTERFACE", fields: map[string]string{"id": "ID!"}, fieldOrder: []string{"id"}},
	"Actor":                                 {name: "Actor", kind: "INTERFACE", fields: map[string]string{"login": "String!", "avatarUrl": "URI", "url": "URI!"}, fieldOrder: []string{"login", "avatarUrl", "url"}},
	"Label":                                 {name: "Label", kind: "OBJECT", fields: map[string]string{"id": "ID!", "name": "String!", "color": "String!"}, fieldOrder: []string{"id", "name", "color"}},
	"CreateIssuePayload":                    {name: "CreateIssuePayload", kind: "OBJECT", fields: map[string]string{"issue": "Issue"}, fieldOrder: []string{"issue"}},
	"CloseIssuePayload":                     {name: "CloseIssuePayload", kind: "OBJECT", fields: map[string]string{"issue": "Issue"}, fieldOrder: []string{"issue"}},
	"CreatePullRequestPayload":              {name: "CreatePullRequestPayload", kind: "OBJECT", fields: map[string]string{"pullRequest": "PullRequest"}, fieldOrder: []string{"pullRequest"}},
	"MergePullRequestPayload":               {name: "MergePullRequestPayload", kind: "OBJECT", fields: map[string]string{"pullRequest": "PullRequest"}, fieldOrder: []string{"pullRequest"}},
	"AddCommentPayload":                     {name: "AddCommentPayload", kind: "OBJECT", fields: map[string]string{"commentEdge": "IssueCommentEdge"}, fieldOrder: []string{"commentEdge"}},
	"IssueCommentEdge":                      {name: "IssueCommentEdge", kind: "OBJECT", fields: map[string]string{"node": "IssueComment"}, fieldOrder: []string{"node"}},
	"IssueState":                            {name: "IssueState", kind: "ENUM", enumValues: []string{"OPEN", "CLOSED"}},
	"PullRequestState":                      {name: "PullRequestState", kind: "ENUM", enumValues: []string{"OPEN", "CLOSED", "MERGED"}},
	"MergeableState":                        {name: "MergeableState", kind: "ENUM", enumValues: []string{"MERGEABLE", "CONFLICTING", "UNKNOWN"}},
	"PullRequestMergeMethod":                {name: "PullRequestMergeMethod", kind: "ENUM", enumValues: []string{"MERGE", "SQUASH", "REBASE"}},
	"RepositoryPermission":                  {name: "RepositoryPermission", kind: "ENUM", enumValues: []string{"ADMIN", "MAINTAIN", "WRITE", "TRIAGE", "READ"}},
	"SearchType":                            {name: "SearchType", kind: "ENUM", enumValues: []string{"ISSUE", "REPOSITORY", "USER", "DISCUSSION"}},
	"IssueOrderField":                       {name: "IssueOrderField", kind: "ENUM", enumValues: []string{"CREATED_AT", "UPDATED_AT", "COMMENTS"}},
	"OrderDirection":                        {name: "OrderDirection", kind: "ENUM", enumValues: []string{"ASC", "DESC"}},
	"__Type":                                {name: "__Type", kind: "OBJECT", fields: map[string]string{"name": "String", "kind": "__TypeKind!", "fields": "[__Field!]", "enumValues": "[__EnumValue!]"}, fieldOrder: []string{"name", "kind", "fields", "enumValues"}},
	"__Field":                               {name: "__Field", kind: "OBJECT", fields: map[string]string{"name": "String!", "type": "__Type!"}, fieldOrder: []string{"name", "type"}},
	"__EnumValue":                           {name: "__EnumValue", kind: "OBJECT", fields: map[string]string{"name": "String!"}, fieldOrder: []string{"name"}},
	"__Schema":                              {name: "__Schema", kind: "OBJECT", fields: map[string]string{"queryType": "__Type!", "types": "[__Type!]"}, fieldOrder: []string{"queryType", "types"}},
}

func introspectType(name string) *introspectTypeDef {
	return introspectionTypes[name]
}
