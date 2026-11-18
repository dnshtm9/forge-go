// REST v3: the /api/v3 surface — repositories, branches, commits, contents, labels,
// issues, comments and pull requests, and the protocol discipline (rest.ts).
package main

import (
	"encoding/base64"
	"sort"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------- JSON encoding

// jsonBuf builds JSON the way the TypeScript forge's JSON.stringify does: object
// keys in insertion order, strings escaped as JS escapes them.
type jsonBuf struct {
	buf     []byte
	firstKey []bool // one "first key" flag per open object level
}

func newJSON() *jsonBuf { return &jsonBuf{firstKey: []bool{true}} }

func (j *jsonBuf) Bytes() []byte { return j.buf }

func (j *jsonBuf) raw(s string) { j.buf = append(j.buf, s...) }

func (j *jsonBuf) sep() {
	if !j.firstKey[len(j.firstKey)-1] {
		j.raw(",")
	}
	j.firstKey[len(j.firstKey)-1] = false
}

// begin opens a nested object (or array context) for sep purposes.
func (j *jsonBuf) begin() { j.firstKey = append(j.firstKey, true) }

// end closes one nesting level.
func (j *jsonBuf) end() { j.firstKey = j.firstKey[:len(j.firstKey)-1] }

func (j *jsonBuf) key(name string) {
	j.sep()
	j.string(name)
	j.raw(":")
}

func (j *jsonBuf) string(s string) {
	j.buf = appendJSONString(j.buf, s)
}

func (j *jsonBuf) int(v int)  { j.raw(strconv.Itoa(v)) }
func (j *jsonBuf) int64(v int64) { j.raw(strconv.FormatInt(v, 10)) }
func (j *jsonBuf) boolean(v bool) {
	if v {
		j.raw("true")
	} else {
		j.raw("false")
	}
}
func (j *jsonBuf) null() { j.raw("null") }

func (j *jsonBuf) strOrNull(s string, has bool) {
	if has {
		j.string(s)
	} else {
		j.null()
	}
}

func (j *jsonBuf) strPtr(p *string) {
	if p != nil {
		j.string(*p)
	} else {
		j.null()
	}
}

// jv serializes a parsed jsonValue (keys in their parsed order).
func (j *jsonBuf) jv(v jsonValue) {
	switch v.kind {
	case jsonNull:
		j.null()
	case jsonBool:
		j.boolean(v.boolean)
	case jsonNumber:
		j.buf = appendJSONNumber(j.buf, v.num)
	case jsonString:
		j.string(v.str)
	case jsonArray:
		j.raw("[")
		for i, item := range v.items {
			if i > 0 {
				j.raw(",")
			}
			j.jv(item)
		}
		j.raw("]")
	case jsonObject:
		j.raw("{")
		j.begin()
		for i, m := range v.members {
			if i > 0 {
				j.raw(",")
			}
			j.string(m.key)
			j.raw(":")
			j.jv(m.value)
		}
		j.end()
		j.raw("}")
	}
}

// appendJSONNumber renders a number the way JS does for the values this
// server reads (integers stay integral).
func appendJSONNumber(dst []byte, f float64) []byte {
	if f == float64(int64(f)) {
		return strconv.AppendInt(dst, int64(f), 10)
	}
	return strconv.AppendFloat(dst, f, 'g', -1, 64)
}

// appendJSONString escapes s as JSON.stringify does.
func appendJSONString(dst []byte, s string) []byte {
	dst = append(dst, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			dst = append(dst, '\\', '"')
		case c == '\\':
			dst = append(dst, '\\', '\\')
		case c == '\n':
			dst = append(dst, '\\', 'n')
		case c == '\r':
			dst = append(dst, '\\', 'r')
		case c == '\t':
			dst = append(dst, '\\', 't')
		case c == '\b':
			dst = append(dst, '\\', 'b')
		case c == '\f':
			dst = append(dst, '\\', 'f')
		case c < 0x20:
			dst = append(dst, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xf])
		default:
			dst = append(dst, c)
		}
	}
	dst = append(dst, '"')
	return dst
}

const hexDigits = "0123456789abcdef"

// ---------------------------------------------------------------- shapes

// nodeId: the deterministic Relay-style global id rest.ts builds (a double base64).
func nodeId(typeName, id string) string {
	inner := base64.StdEncoding.EncodeToString([]byte(`["` + typeName + `","` + id + `"]`))
	return base64.StdEncoding.EncodeToString([]byte("012:" + typeName + inner))
}

// userRef answers the read-only user ref object's JSON.
func userRefJSON(f *forge, login string) []byte {
	u, _ := f.user(login)
	id := 0
	if u != nil {
		id = u.id
	}
	j := newJSON()
	j.raw("{")
	j.begin()
	j.key("login")
	j.string(login)
	j.key("id")
	j.int(id)
	j.key("node_id")
	j.string(nodeId("User", strconv.Itoa(id)))
	j.key("avatar_url")
	j.string("https://avatars.example.invalid/u/" + login + "?v=4")
	j.key("gravatar_id")
	j.raw(`""`)
	j.key("url")
	j.string("/api/v3/users/" + login)
	j.key("html_url")
	j.string("https://example.invalid/" + login)
	j.key("type")
	j.string("User")
	j.key("site_admin")
	j.boolean(false)
	j.end()
	j.raw("}")
	return j.Bytes()
}

// userRefWithDates answers GET /user's body.
func userRefWithDatesJSON(f *forge, login string, iso string) []byte {
	j := newJSON()
	j.raw("{")
	j.begin()
	j.key("login")
	j.string(login)
	u, _ := f.user(login)
	id := 0
	if u != nil {
		id = u.id
	}
	j.key("id")
	j.int(id)
	j.key("node_id")
	j.string(nodeId("User", strconv.Itoa(id)))
	j.key("avatar_url")
	j.string("https://avatars.example.invalid/u/" + login + "?v=4")
	j.key("gravatar_id")
	j.raw(`""`)
	j.key("url")
	j.string("/api/v3/users/" + login)
	j.key("html_url")
	j.string("https://example.invalid/" + login)
	j.key("type")
	j.string("User")
	j.key("site_admin")
	j.boolean(false)
	j.key("created_at")
	j.string(iso)
	j.key("updated_at")
	j.string(iso)
	j.end()
	j.raw("}")
	return j.Bytes()
}

func repoJSONBytes(f *forge, r *repo) []byte {
	fullName := r.owner + "/" + r.name
	iso := f.clock.isoString()
	j := newJSON()
	j.raw("{")
	j.begin()
	j.key("id")
	j.int(r.id)
	j.key("node_id")
	j.string(nodeId("Repository", strconv.Itoa(r.id)))
	j.key("name")
	j.string(r.name)
	j.key("full_name")
	j.string(fullName)
	j.key("private")
	j.boolean(r.privateFlag)
	j.key("owner")
	j.buf = append(j.buf, userRefJSON(f, r.owner)...)
	j.key("html_url")
	j.string("https://example.invalid/" + fullName)
	j.key("description")
	j.string(r.description)
	j.key("fork")
	j.boolean(false)
	j.key("url")
	j.string("/api/v3/repos/" + fullName)
	j.key("created_at")
	j.string(iso)
	j.key("updated_at")
	j.string(iso)
	j.key("pushed_at")
	j.string(iso)
	j.key("default_branch")
	j.string(r.defaultBranch)
	j.key("has_issues")
	j.boolean(true)
	j.key("has_projects")
	j.boolean(false)
	j.key("has_wiki")
	j.boolean(false)
	j.key("has_pages")
	j.boolean(false)
	j.key("has_downloads")
	j.boolean(false)
	j.key("archived")
	j.boolean(false)
	j.key("disabled")
	j.boolean(false)
	j.key("visibility")
	if r.privateFlag {
		j.string("private")
	} else {
		j.string("public")
	}
	j.end()
	j.raw("}")
	return j.Bytes()
}

func labelJSONBytes(l *label) []byte {
	j := newJSON()
	j.raw("{")
	j.begin()
	j.key("id")
	j.int(l.id)
	j.key("node_id")
	j.string(nodeId("Label", strconv.Itoa(l.id)))
	j.key("url")
	j.raw(`""`)
	j.key("name")
	j.string(l.name)
	j.key("color")
	j.string(l.color)
	j.key("default")
	j.boolean(false)
	j.key("description")
	j.null()
	j.end()
	j.raw("}")
	return j.Bytes()
}

// issueJSONBytes serializes one issue (rest.ts issueJson).
func issueJSONBytes(f *forge, r *repo, is *issue) []byte {
	j := newJSON()
	j.raw("{")
	j.begin()
	j.key("id")
	j.int(is.id)
	j.key("node_id")
	j.string(nodeId("Issue", strconv.Itoa(r.id)+"I"+strconv.Itoa(is.number)))
	j.key("number")
	j.int(is.number)
	j.key("title")
	j.string(is.title)
	j.key("user")
	j.buf = append(j.buf, userRefJSON(f, is.user)...)
	j.key("labels")
	j.raw("[")
	for i, name := range is.labels {
		if i > 0 {
			j.raw(",")
		}
		if l := f.labelByName(r, name); l != nil {
			j.buf = append(j.buf, labelJSONBytes(l)...)
		} else {
			j.raw(`{"id":0,"node_id":"","url":"","name":`)
			j.buf = appendJSONString(j.buf, name)
			j.raw(`,"color":"ededed","default":false,"description":null}`)
		}
	}
	j.raw("]")
	j.key("state")
	j.string(is.state)
	j.key("locked")
	j.boolean(false)
	j.key("assignee")
	j.null()
	j.key("assignees")
	j.raw("[]")
	j.key("comments")
	j.int(len(f.commentsOf(r, is.number)))
	j.key("created_at")
	j.string(is.createdAt)
	j.key("updated_at")
	j.string(is.updatedAt)
	j.key("closed_at")
	j.strPtr(is.closedAt)
	j.key("author_association")
	j.string("OWNER")
	j.key("body")
	j.string(is.body)
	if is.isPull {
		j.key("pull_request")
		j.raw("{")
		j.begin()
		j.key("url")
		j.string("/api/v3/repos/" + r.owner + "/" + r.name + "/pulls/" + strconv.Itoa(*is.pullNumber))
		j.key("html_url")
		j.raw(`""`)
		j.key("diff_url")
		j.raw(`""`)
		j.key("patch_url")
		j.raw(`""`)
		j.key("merged_at")
		j.null()
		j.end()
		j.raw("}")
	}
	j.end()
	j.raw("}")
	return j.Bytes()
}

func pullJSONBytes(f *forge, r *repo, p *pull) []byte {
	j := newJSON()
	j.raw("{")
	j.begin()
	j.key("id")
	j.int(p.id)
	j.key("node_id")
	j.string(nodeId("PullRequest", strconv.Itoa(r.id)+"P"+strconv.Itoa(p.number)))
	j.key("number")
	j.int(p.number)
	j.key("state")
	j.string(p.state)
	j.key("locked")
	j.boolean(false)
	j.key("title")
	j.string(p.title)
	j.key("user")
	j.buf = append(j.buf, userRefJSON(f, p.user)...)
	j.key("body")
	j.string(p.body)
	j.key("created_at")
	j.string(p.createdAt)
	j.key("updated_at")
	j.string(p.updatedAt)
	j.key("closed_at")
	if p.state == "closed" {
		j.string(p.updatedAt)
	} else {
		j.null()
	}
	j.key("merged_at")
	j.strPtr(p.mergedAt)
	j.key("merge_commit_sha")
	j.strPtr(p.mergeCommitSha)
	j.key("assignee")
	j.null()
	j.key("assignees")
	j.raw("[]")
	j.key("requested_reviewers")
	j.raw("[]")
	j.key("labels")
	j.raw("[]")
	j.key("head")
	j.raw("{")
	j.begin()
	j.key("label")
	j.string(r.owner + ":" + p.head)
	j.key("ref")
	j.string(p.head)
	j.key("sha")
	j.string(p.headSha)
	j.key("repo")
	j.raw("{")
	j.begin()
	j.key("name")
	j.string(r.name)
	j.key("full_name")
	j.string(r.owner + "/" + r.name)
	j.key("private")
	j.boolean(r.privateFlag)
	j.end()
	j.raw("}")
	j.end()
	j.raw("}")
	j.key("base")
	j.raw("{")
	j.begin()
	j.key("label")
	j.string(r.owner + ":" + p.base)
	j.key("ref")
	j.string(p.base)
	j.key("sha")
	j.string(p.baseSha)
	j.key("repo")
	j.raw("{")
	j.begin()
	j.key("name")
	j.string(r.name)
	j.key("full_name")
	j.string(r.owner + "/" + r.name)
	j.key("private")
	j.boolean(r.privateFlag)
	j.end()
	j.raw("}")
	j.end()
	j.raw("}")
	j.key("merged")
	j.boolean(p.merged)
	j.key("mergeable")
	if p.mergeable == "CONFLICTING" {
		j.boolean(false)
	} else if p.state == "open" && !p.merged {
		j.boolean(true)
	} else {
		j.null()
	}
	j.key("mergeable_state")
	if p.merged {
		j.string("clean")
	} else if p.mergeable == "CONFLICTING" {
		j.string("dirty")
	} else {
		j.string("clean")
	}
	j.key("draft")
	j.boolean(false)
	j.key("author_association")
	j.string("OWNER")
	j.end()
	j.raw("}")
	return j.Bytes()
}

func commentJSONBytes(f *forge, r *repo, c *comment) []byte {
	j := newJSON()
	j.raw("{")
	j.begin()
	j.key("id")
	j.int(c.id)
	j.key("node_id")
	j.string(nodeId("IssueComment", strconv.Itoa(c.id)))
	j.key("url")
	j.string("/api/v3/repos/" + r.owner + "/" + r.name + "/issues/" + strconv.Itoa(c.issueNumber) + "/comments")
	j.key("html_url")
	j.raw(`""`)
	j.key("body")
	j.string(c.body)
	j.key("user")
	j.buf = append(j.buf, userRefJSON(f, c.user)...)
	j.key("created_at")
	j.string(c.createdAt)
	j.key("updated_at")
	j.string(c.updatedAt)
	j.key("author_association")
	j.string("OWNER")
	j.key("issue_url")
	j.string("/api/v3/repos/" + r.owner + "/" + r.name + "/issues/" + strconv.Itoa(c.issueNumber))
	j.end()
	j.raw("}")
	return j.Bytes()
}

// dateOf converts a git identity line's `1234567890 +0000` tail to ISO.
func dateOf(line string) string {
	// match /(\d+) ([+-]\d{4})$/
	i := len(line)
	// find trailing timezone
	tzStart := -1
	for k := len(line) - 1; k >= 0; k-- {
		c := line[k]
		if c == '+' || c == '-' {
			if len(line)-k == 5 {
				tzStart = k
			}
			break
		}
		if !(c >= '0' && c <= '9') {
			break
		}
	}
	if tzStart <= 0 {
		return rfc3339FromMs(1_735_689_600_000)
	}
	_ = i
	secText := strings.TrimRight(line[:tzStart-1], " ")
	// the seconds are the digits right before the timezone
	sp := strings.LastIndexByte(secText, ' ')
	if sp >= 0 {
		secText = secText[sp+1:]
	}
	sec, err := strconv.ParseInt(secText, 10, 64)
	if err != nil {
		return rfc3339FromMs(1_735_689_600_000)
	}
	return rfc3339FromMs(sec * 1000)
}

func authorName(line string) string {
	open := strings.LastIndex(line, " <")
	if open < 0 {
		return line
	}
	return line[:open]
}

func authorEmail(line string) string {
	open := strings.LastIndex(line, " <")
	close := strings.LastIndexByte(line, '>')
	if open < 0 || close < 0 {
		return ""
	}
	return line[open+2 : close]
}

func commitJSONBytes(f *forge, r *repo, sha string) ([]byte, bool) {
	object, ok := r.objects[sha]
	if !ok || object.typ != typeCommit {
		return nil, false
	}
	c, err := parseCommit(object.data)
	if err != nil {
		return nil, false
	}
	message := strings.TrimRight(c.message, "\n")
	j := newJSON()
	j.raw("{")
	j.begin()
	j.key("sha")
	j.string(sha)
	j.key("node_id")
	j.string(nodeId("Commit", sha))
	j.key("commit")
	j.raw("{")
	j.begin()
	j.key("author")
	j.raw("{")
	j.begin()
	j.key("name")
	j.string(authorName(c.author))
	j.key("email")
	j.string(authorEmail(c.author))
	j.key("date")
	j.string(dateOf(c.author))
	j.end()
	j.raw("}")
	j.key("committer")
	j.raw("{")
	j.begin()
	j.key("name")
	j.string(authorName(c.committer))
	j.key("email")
	j.string(authorEmail(c.committer))
	j.key("date")
	j.string(dateOf(c.committer))
	j.end()
	j.raw("}")
	j.key("message")
	j.string(message)
	j.key("tree")
	j.raw(`{"sha":`)
	j.string(c.tree)
	j.raw(`,"url":""}`)
	j.key("url")
	j.raw(`""`)
	j.key("comment_count")
	j.int(0)
	j.end()
	j.raw("}")
	j.key("url")
	j.raw(`""`)
	j.key("html_url")
	j.raw(`""`)
	j.key("parents")
	j.raw("[")
	for i, parent := range c.parents {
		if i > 0 {
			j.raw(",")
		}
		j.raw(`{"sha":`)
		j.string(parent)
		j.raw(`,"url":"","html_url":""}`)
	}
	j.raw("]")
	j.end()
	j.raw("}")
	return j.Bytes(), true
}

func branchJSONBytes(r *repo, name string) []byte {
	oid := r.refs["refs/heads/"+name]
	j := newJSON()
	j.raw("{")
	j.begin()
	j.key("name")
	j.string(name)
	j.key("commit")
	j.raw(`{"sha":`)
	j.string(oid)
	j.raw(`,"url":""}`)
	j.key("protected")
	j.boolean(false)
	j.end()
	j.raw("}")
	return j.Bytes()
}

func base64Of(data []byte) string { return base64.StdEncoding.EncodeToString(data) }

// ---------------------------------------------------------------- replies

type reply struct {
	status  int
	body    []byte // nil means 204 (no body)
	headers [][2]string
}

func (rp *reply) header(name, value string) *reply {
	rp.headers = append(rp.headers, [2]string{name, value})
	return rp
}

func jsonReply(status int, body []byte) *reply {
	return &reply{status: status, body: body}
}

func messageErrorBytes(status int, message string) []byte {
	j := newJSON()
	j.raw("{")
	j.begin()
	j.key("message")
	j.string(message)
	if status == 404 {
		j.key("documentation_url")
		j.string("https://docs.github.com/rest")
	}
	j.end()
	j.raw("}")
	return j.Bytes()
}

func notFoundReply() *reply {
	return jsonReply(404, messageErrorBytes(404, "Not Found"))
}

func problemsParsingReply() *reply {
	return jsonReply(400, messageErrorBytes(400, "Problems parsing JSON"))
}

// ---------------------------------------------------------------- request view

type restRequest struct {
	method   string
	path     string // decoded path segments already split
	query    map[string]string
	queryKeys []string // sorted query keys (deterministic reads)
	body     jsonValue
	bodyRaw  string
	hasToken bool
	host     string
}

// ---------------------------------------------------------------- the JSON value model

type jsonKind int

const (
	jsonNull jsonKind = iota
	jsonBool
	jsonNumber
	jsonString
	jsonArray
	jsonObject
)

type jsonMember struct {
	key   string
	value jsonValue
}

type jsonValue struct {
	kind    jsonKind
	str     string
	num     float64
	boolean bool
	items   []jsonValue
	members []jsonMember
}

func (v *jsonValue) isObject() bool { return v.kind == jsonObject }
func (v *jsonValue) asObject() *jsonValue {
	if v.kind == jsonObject {
		return v
	}
	return nil
}


// chainMember answers one member's value, or an empty jsonValue when absent
// (the handful of `.member(...).value` reads the request bodies use).
func (v *jsonValue) chainMember(key string) jsonValue {
	for i := range v.members {
		if v.members[i].key == key {
			return v.members[i].value
		}
	}
	return jsonValue{}
}

func (v *jsonValue) member(key string) (jsonValue, bool) {
	for i := range v.members {
		if v.members[i].key == key {
			return v.members[i].value, true
		}
	}
	return jsonValue{}, false
}

func (v jsonValue) strV() string {
	if v.kind == jsonString {
		return v.str
	}
	return ""
}

// parseJSON parses a complete JSON document (RFC 8259) into a jsonValue.
// It answers ok=false for any malformed input, matching JSON.parse's throw.
func parseJSON(text string) (jsonValue, bool) {
	p := &jsonParser{src: text}
	v, ok := p.parseValue()
	if !ok {
		return jsonValue{}, false
	}
	p.skipWS()
	if p.pos != len(p.src) {
		return jsonValue{}, false
	}
	return v, true
}

type jsonParser struct {
	src string
	pos int
}

func (p *jsonParser) skipWS() {
	for p.pos < len(p.src) {
		switch p.src[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

func (p *jsonParser) peek() byte {
	if p.pos < len(p.src) {
		return p.src[p.pos]
	}
	return 0
}

func (p *jsonParser) parseValue() (jsonValue, bool) {
	p.skipWS()
	if p.pos >= len(p.src) {
		return jsonValue{}, false
	}
	switch c := p.src[p.pos]; {
	case c == '{':
		return p.parseObject()
	case c == '[':
		return p.parseArray()
	case c == '"':
		s, ok := p.parseString()
		if !ok {
			return jsonValue{}, false
		}
		return jsonValue{kind: jsonString, str: s}, true
	case c == 't':
		if strings.HasPrefix(p.src[p.pos:], "true") {
			p.pos += 4
			return jsonValue{kind: jsonBool, boolean: true}, true
		}
		return jsonValue{}, false
	case c == 'f':
		if strings.HasPrefix(p.src[p.pos:], "false") {
			p.pos += 5
			return jsonValue{kind: jsonBool, boolean: false}, true
		}
		return jsonValue{}, false
	case c == 'n':
		if strings.HasPrefix(p.src[p.pos:], "null") {
			p.pos += 4
			return jsonValue{kind: jsonNull}, true
		}
		return jsonValue{}, false
	case c == '-' || (c >= '0' && c <= '9'):
		return p.parseNumber()
	}
	return jsonValue{}, false
}

func (p *jsonParser) parseObject() (jsonValue, bool) {
	p.pos++ // '{'
	out := jsonValue{kind: jsonObject}
	p.skipWS()
	if p.peek() == '}' {
		p.pos++
		return out, true
	}
	for {
		p.skipWS()
		if p.peek() != '"' {
			return jsonValue{}, false
		}
		key, ok := p.parseString()
		if !ok {
			return jsonValue{}, false
		}
		p.skipWS()
		if p.peek() != ':' {
			return jsonValue{}, false
		}
		p.pos++
		value, ok := p.parseValue()
		if !ok {
			return jsonValue{}, false
		}
		out.members = append(out.members, jsonMember{key: key, value: value})
		p.skipWS()
		switch p.peek() {
		case ',':
			p.pos++
		case '}':
			p.pos++
			return out, true
		default:
			return jsonValue{}, false
		}
	}
}

func (p *jsonParser) parseArray() (jsonValue, bool) {
	p.pos++ // '['
	out := jsonValue{kind: jsonArray}
	p.skipWS()
	if p.peek() == ']' {
		p.pos++
		return out, true
	}
	for {
		value, ok := p.parseValue()
		if !ok {
			return jsonValue{}, false
		}
		out.items = append(out.items, value)
		p.skipWS()
		switch p.peek() {
		case ',':
			p.pos++
		case ']':
			p.pos++
			return out, true
		default:
			return jsonValue{}, false
		}
	}
}

func (p *jsonParser) parseString() (string, bool) {
	p.pos++ // '"'
	start := p.pos
	// fast scan for simple strings
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		if c == '"' {
			s := p.src[start:p.pos]
			p.pos++
			return s, true
		}
		if c == '\\' || c < 0x20 {
			return p.parseStringSlow(start)
		}
		p.pos++
	}
	return "", false
}

func (p *jsonParser) parseStringSlow(start int) (string, bool) {
	var buf []byte
	buf = append(buf, p.src[start:p.pos]...)
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		if c == '"' {
			p.pos++
			return string(buf), true
		}
		if c == '\\' {
			p.pos++
			if p.pos >= len(p.src) {
				return "", false
			}
			e := p.src[p.pos]
			p.pos++
			switch e {
			case '"':
				buf = append(buf, '"')
			case '\\':
				buf = append(buf, '\\')
			case '/':
				buf = append(buf, '/')
			case 'b':
				buf = append(buf, '\b')
			case 'f':
				buf = append(buf, '\f')
			case 'n':
				buf = append(buf, '\n')
			case 'r':
				buf = append(buf, '\r')
			case 't':
				buf = append(buf, '\t')
			case 'u':
				if p.pos+4 > len(p.src) {
					return "", false
				}
				r, ok := parseHex4(p.src[p.pos : p.pos+4])
				if !ok {
					return "", false
				}
				p.pos += 4
				if r >= 0xD800 && r <= 0xDBFF && p.pos+6 <= len(p.src) && p.src[p.pos] == '\\' && p.src[p.pos+1] == 'u' {
					r2, ok := parseHex4(p.src[p.pos+2 : p.pos+6])
					if ok && r2 >= 0xDC00 && r2 <= 0xDFFF {
						p.pos += 6
						r = 0x10000 + (r-0xD800)<<10 + (r2 - 0xDC00)
					}
				}
				buf = appendRune(buf, r)
			default:
				return "", false
			}
			continue
		}
		if c < 0x20 {
			return "", false
		}
		buf = append(buf, c)
		p.pos++
	}
	return "", false
}

func parseHex4(s string) (int, bool) {
	if len(s) != 4 {
		return 0, false
	}
	v := 0
	for i := 0; i < 4; i++ {
		c := s[i]
		var d int
		switch {
		case c >= '0' && c <= '9':
			d = int(c - '0')
		case c >= 'a' && c <= 'f':
			d = int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = int(c-'A') + 10
		default:
			return 0, false
		}
		v = v<<4 | d
	}
	return v, true
}

func appendRune(dst []byte, r int) []byte {
	if r < 0x80 {
		return append(dst, byte(r))
	}
	if r < 0x800 {
		return append(dst, byte(0xC0|r>>6), byte(0x80|r&0x3F))
	}
	if r < 0x10000 {
		return append(dst, byte(0xE0|r>>12), byte(0x80|(r>>6)&0x3F), byte(0x80|r&0x3F))
	}
	return append(dst, byte(0xF0|r>>18), byte(0x80|(r>>12)&0x3F), byte(0x80|(r>>6)&0x3F), byte(0x80|r&0x3F))
}

func (p *jsonParser) parseNumber() (jsonValue, bool) {
	start := p.pos
	if p.peek() == '-' {
		p.pos++
	}
	digits := false
	for p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
		p.pos++
		digits = true
	}
	if p.peek() == '.' {
		p.pos++
		for p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
			p.pos++
		}
	}
	if p.peek() == 'e' || p.peek() == 'E' {
		p.pos++
		if p.peek() == '+' || p.peek() == '-' {
			p.pos++
		}
		for p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
			p.pos++
		}
	}
	if !digits {
		return jsonValue{}, false
	}
	text := p.src[start:p.pos]
	n, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return jsonValue{}, false
	}
	return jsonValue{kind: jsonNumber, num: n}, true
}

// ---------------------------------------------------------------- helpers

func str(v jsonValue) string { return v.strV() }

func asObjectBody(v jsonValue) *jsonValue {
	if v.kind == jsonObject {
		return &v
	}
	return nil
}

func clampInt(text string, has bool, min, max, fallback int) int {
	if !has || text == "" {
		return fallback
	}
	n, err := strconv.Atoi(text)
	if err != nil {
		return fallback
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

// decodePath percent-decodes a whole path segment sequence (rest.ts decodePath).
func decodePath(path string) string {
	if !strings.Contains(path, "%") && !strings.Contains(path, "+") {
		return path
	}
	// decodeURIComponent semantics: '+' is NOT a space.
	out := make([]byte, 0, len(path))
	for i := 0; i < len(path); i++ {
		c := path[i]
		if c == '%' && i+2 < len(path) {
			h1, ok1 := hexVal(path[i+1])
			h2, ok2 := hexVal(path[i+2])
			if ok1 && ok2 {
				out = append(out, byte(h1<<4|h2))
				i += 2
				continue
			}
		}
		out = append(out, c)
	}
	return string(out)
}

func hexVal(c byte) (int, bool) {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0'), true
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10, true
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10, true
	}
	return 0, false
}

func refTip(r *repo, refName string) (string, bool) {
	oid, ok := r.refs[refName]
	return oid, ok
}

func commitOfRef(r *repo, ref string) (string, bool) {
	name := ref
	if !strings.HasPrefix(ref, "refs/") {
		name = "refs/heads/" + ref
	}
	if oid, ok := r.refs[name]; ok {
		return oid, true
	}
	if isObjectId(ref) {
		return ref, true
	}
	return "", false
}

// commitContents builds a commit object for a contents change (rest.ts commitContents).
func commitContents(f *forge, r *repo, parent, treeOid, message string) string {
	who := f.viewerLogin + " <" + f.viewerLogin + "@example.invalid>"
	var lines []string
	lines = append(lines, "tree "+treeOid)
	if parent != "" {
		lines = append(lines, "parent "+parent)
	}
	lines = append(lines, "author "+who+" "+strconv.FormatInt(f.clock.nowSec(), 10)+" +0000")
	lines = append(lines, "committer "+who+" "+strconv.FormatInt(f.clock.nowSec(), 10)+" +0000")
	lines = append(lines, "")
	if !strings.HasSuffix(message, "\n") {
		message += "\n"
	}
	lines = append(lines, message)
	data := []byte(strings.Join(lines, "\n"))
	return f.putObject(r, typeCommit, data)
}

func treeEntriesOf(r *repo, treeOid string) []treeEntry {
	tree, ok := r.objects[treeOid]
	if !ok || tree.typ != typeTree {
		return nil
	}
	entries, err := parseTree(tree.data)
	if err != nil {
		return nil
	}
	return entries
}

func treeEntries(r *repo, commitSha string) []treeEntry {
	if commitSha == "" {
		return nil
	}
	commitObject, ok := r.objects[commitSha]
	if !ok || commitObject.typ != typeCommit {
		return nil
	}
	c, err := parseCommit(commitObject.data)
	if err != nil {
		return nil
	}
	return treeEntriesOf(r, c.tree)
}

// setTreePath sets path to blobOid in a commit's tree, building nested trees.
func setTreePath(f *forge, r *repo, entries []treeEntry, segments []string, blobOid string) string {
	head := segments[0]
	rest := segments[1:]
	var kept []treeEntry
	for _, e := range entries {
		if e.name != head {
			kept = append(kept, e)
		}
	}
	if len(rest) == 0 {
		if blobOid != "" {
			kept = append(kept, treeEntry{mode: "100644", name: head, oid: blobOid})
		}
	} else {
		var childEntries []treeEntry
		for _, e := range entries {
			if e.name == head && e.mode == "40000" {
				childEntries = treeEntriesOf(r, e.oid)
				break
			}
		}
		childOid := setTreePath(f, r, childEntries, rest, blobOid)
		kept = append(kept, treeEntry{mode: "40000", name: head, oid: childOid})
	}
	data, err := buildTree(kept)
	if err != nil {
		return ""
	}
	return f.putObject(r, typeTree, data)
}

// findPath finds a path in a tree; answers its entry or false.
func findEntryIn(r *repo, entries []treeEntry, segments []string) (treeEntry, bool) {
	current := entries
	var found treeEntry
	for index, segment := range segments {
		var next *treeEntry
		for i := range current {
			if current[i].name == segment {
				next = &current[i]
				break
			}
		}
		if next == nil {
			return treeEntry{}, false
		}
		if index == len(segments)-1 {
			return *next, true
		}
		if next.mode != "40000" {
			return treeEntry{}, false
		}
		current = treeEntriesOf(r, next.oid)
	}
	return found, false
}

// ---------------------------------------------------------------- routing

// handleRest answers one /api/v3 request (path already split into decoded segments).
func handleRest(f *forge, req *restRequest) *reply {
	decoded := req.path
	segments := splitPathSegments(decoded)
	method := req.method

	if len(segments) == 0 {
		if method != "GET" {
			return notFoundReply()
		}
		j := newJSON()
		j.raw("{")
		j.begin()
		j.key("current_user_url")
		j.string("/api/v3/user")
		j.key("authorizations_url")
		j.string("/api/v3/authorizations")
		j.key("code_search_url")
		j.string("/api/v3/search/code?q={query}{&page,per_page,sort,order}")
		j.key("emails_url")
		j.string("/api/v3/user/emails")
		j.key("events_url")
		j.string("/api/v3/events")
		j.key("feeds_url")
		j.string("/api/v3/feeds")
		j.key("followers_url")
		j.string("/api/v3/user/followers")
		j.key("issue_search_url")
		j.string("/api/v3/search/issues?q={query}{&page,per_page,sort,order}")
		j.key("issues_url")
		j.string("/api/v3/issues")
		j.key("repository_url")
		j.string("/api/v3/repos/{owner}/{repo}")
		j.key("repository_search_url")
		j.string("/api/v3/search/repositories?q={query}{&page,per_page,sort,order}")
		j.key("user_url")
		j.string("/api/v3/users/{user}")
		j.key("user_repositories_url")
		j.string("/api/v3/users/{user}/repos{?type,page,per_page,sort}")
		j.end()
		j.raw("}")
		return jsonReply(200, j.Bytes()).
			header("x-oauth-scopes", "repo, read:org").
			header("x-accepted-oauth-scopes", "repo").
			header("x-github-media-type", "github.v3; format=json")
	}
	if segments[0] == "meta" && len(segments) == 1 {
		return jsonReply(200, []byte(`{"verifiable_password_authentication":true,"installed_version":"forge-core-1.0.0"}`))
	}
	if segments[0] == "user" && len(segments) == 1 {
		return jsonReply(200, userRefWithDatesJSON(f, f.viewerLogin, f.clock.isoString()))
	}
	if segments[0] == "user" && len(segments) >= 2 && segments[1] == "repos" && len(segments) == 2 {
		if method == "POST" {
			return createUserRepo(f, req)
		}
		if method == "GET" {
			return listUserRepos(f, req)
		}
		return notFoundReply()
	}
	if segments[0] == "repos" && len(segments) >= 3 {
		owner := segments[1]
		name := segments[2]
		r, ok := f.getRepo(owner, name)
		if !ok || owner != f.viewerLogin {
			return notFoundReply()
		}
		return handleRepo(f, r, segments[3:], req)
	}
	return notFoundReply()
}

// splitPathSegments splits a path on '/', dropping empty segments (the
// `split('/').filter(s => s !== '')` of rest.ts).
func splitPathSegments(path string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(path); i++ {
		if i == len(path) || path[i] == '/' {
			if i > start {
				out = append(out, path[start:i])
			}
			start = i + 1
		}
	}
	return out
}

func createUserRepo(f *forge, req *restRequest) *reply {
	body := asObjectBody(req.body)
	if body == nil {
		return problemsParsingReply()
	}
	name := strings.TrimSpace(body.chainMember("name").strV())
	if name == "" {
		return jsonReply(422, messageErrorBytes(422, "Validation Failed"))
	}
	if !validRepoName(name) || strings.HasSuffix(name, ".git") {
		return jsonReply(422, messageErrorBytes(422, "Validation Failed"))
	}
	if _, exists := f.getRepo(f.viewerLogin, name); exists {
		return jsonReply(422, messageErrorBytes(422, "name already exists on this account"))
	}
	description := body.chainMember("description").strV()
	privateFlag := body.chainMember("private").kind == jsonBool && body.chainMember("private").boolean
	r := f.createRepo(f.viewerLogin, name, description, privateFlag)
	return jsonReply(201, repoJSONBytes(f, r))
}

func validRepoName(name string) bool {
	if len(name) < 1 || len(name) > 100 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func listUserRepos(f *forge, req *restRequest) *reply {
	perPage := clampInt(req.query["per_page"], hasKey(req, "per_page"), 1, 100, 30)
	page := clampInt(req.query["page"], hasKey(req, "page"), 1, 1<<50, 1)
	repos := f.reposOf(f.viewerLogin)
	sortKind := req.query["sort"]
	direction := "desc"
	if v, ok := req.query["direction"]; ok {
		direction = v
	}
	ordered := repos
	if sortKind == "created" || sortKind == "updated" {
		cp := make([]*repo, len(repos))
		copy(cp, repos)
		sort.SliceStable(cp, func(i, j int) bool {
			if direction == "asc" {
				return cp[i].id < cp[j].id
			}
			return cp[i].id > cp[j].id
		})
		ordered = cp
	}
	start := (page - 1) * perPage
	end := start + perPage
	if start > len(ordered) {
		start = len(ordered)
	}
	if end > len(ordered) {
		end = len(ordered)
	}
	j := newJSON()
	j.raw("[")
	for i, r := range ordered[start:end] {
		if i > 0 {
			j.raw(",")
		}
		j.buf = append(j.buf, repoJSONBytes(f, r)...)
	}
	j.raw("]")
	host := req.host
	if host == "" {
		host = "localhost"
	}
	base := "http://" + host + "/api/v3/user/repos?per_page=" + strconv.Itoa(perPage)
	lastPage := (len(repos) + perPage - 1) / perPage
	if lastPage < 1 {
		lastPage = 1
	}
	link := ""
	if page < lastPage {
		link += "<" + base + "&page=" + strconv.Itoa(page+1) + `>; rel="next"`
		link += ", "
	}
	link += "<" + base + "&page=1" + `>; rel="first"`
	link += ", "
	link += "<" + base + "&page=" + strconv.Itoa(lastPage) + `>; rel="last"`
	return jsonReply(200, j.Bytes()).header("Link", link)
}

func hasKey(req *restRequest, key string) bool {
	_, ok := req.query[key]
	return ok
}

func handleRepo(f *forge, r *repo, rest []string, req *restRequest) *reply {
	method := req.method
	if len(rest) == 0 {
		if method == "GET" {
			return jsonReply(200, repoJSONBytes(f, r))
		}
		if method == "PATCH" {
			return patchRepo(f, r, req)
		}
		if method == "DELETE" {
			f.deleteRepo(r.owner, r.name)
			return &reply{status: 204}
		}
		return notFoundReply()
	}
	switch rest[0] {
	case "branches":
		return handleBranches(f, r, rest[1:], req)
	case "commits":
		return handleCommits(f, r, rest[1:], req)
	case "contents":
		return handleContents(f, r, rest[1:], req)
	case "git":
		return handleGitData(f, r, rest[1:], req)
	case "labels":
		return handleLabels(f, r, rest[1:], req)
	case "issues":
		return handleIssues(f, r, rest[1:], req)
	case "pulls":
		return handlePulls(f, r, rest[1:], req)
	}
	return notFoundReply()
}

func patchRepo(f *forge, r *repo, req *restRequest) *reply {
	body := asObjectBody(req.body)
	if body == nil {
		return problemsParsingReply()
	}
	if v, ok := body.member("description"); ok && v.kind == jsonString {
		r.description = v.strV()
	}
	if v, ok := body.member("name"); ok && v.kind == jsonString && v.strV() != r.name {
		return jsonReply(422, messageErrorBytes(422, "Validation Failed"))
	}
	return jsonReply(200, repoJSONBytes(f, r))
}

func handleBranches(f *forge, r *repo, rest []string, req *restRequest) *reply {
	if len(rest) == 0 && req.method == "GET" {
		var names []string
		for ref := range r.refs {
			if strings.HasPrefix(ref, "refs/heads/") {
				names = append(names, ref[len("refs/heads/"):])
			}
		}
		sort.Strings(names)
		j := newJSON()
		j.raw("[")
		for i, name := range names {
			if i > 0 {
				j.raw(",")
			}
			j.buf = append(j.buf, branchJSONBytes(r, name)...)
		}
		j.raw("]")
		return jsonReply(200, j.Bytes())
	}
	if len(rest) == 1 && req.method == "GET" {
		name := decodePath(rest[0])
		if _, ok := r.refs["refs/heads/"+name]; !ok {
			return notFoundReply()
		}
		return jsonReply(200, branchJSONBytes(r, name))
	}
	return notFoundReply()
}

func handleCommits(f *forge, r *repo, rest []string, req *restRequest) *reply {
	if len(rest) != 0 || req.method != "GET" {
		return notFoundReply()
	}
	var tip string
	if sha, ok := req.query["sha"]; ok {
		tip, _ = commitOfRef(r, sha)
	} else {
		tip = r.refs["refs/heads/"+r.defaultBranch]
	}
	var commits [][]byte
	seen := map[string]bool{}
	var queue []string
	if tip != "" {
		queue = append(queue, tip)
	}
	for len(queue) > 0 {
		sha := queue[0]
		queue = queue[1:]
		if seen[sha] {
			continue
		}
		object, ok := r.objects[sha]
		if !ok || object.typ != typeCommit {
			continue
		}
		seen[sha] = true
		if view, ok := commitJSONBytes(f, r, sha); ok {
			commits = append(commits, view)
		}
		if c, err := parseCommit(object.data); err == nil {
			queue = append(queue, c.parents...)
		}
	}
	j := newJSON()
	j.raw("[")
	for i, view := range commits {
		if i > 0 {
			j.raw(",")
		}
		j.buf = append(j.buf, view...)
	}
	j.raw("]")
	return jsonReply(200, j.Bytes())
}

// ---------------------------------------------------------------- contents

func contentEntryJSON(r *repo, path string, entry treeEntry) []byte {
	object, hasObject := r.objects[entry.oid]
	isDir := entry.mode == "40000"
	j := newJSON()
	j.raw("{")
	j.begin()
	j.key("type")
	if isDir {
		j.string("dir")
	} else {
		j.string("file")
	}
	j.key("size")
	if hasObject {
		j.int(len(object.data))
	} else {
		j.int(0)
	}
	j.key("name")
	j.string(entry.name)
	j.key("path")
	j.string(path)
	j.key("sha")
	j.string(entry.oid)
	j.key("url")
	j.raw(`""`)
	j.key("html_url")
	j.raw(`""`)
	j.key("git_url")
	j.raw(`""`)
	j.key("download_url")
	if isDir {
		j.null()
	} else {
		j.raw(`""`)
	}
	if !isDir {
		j.key("content")
		if hasObject {
			j.string(base64Of(object.data))
		} else {
			j.string("")
		}
		j.key("encoding")
		j.string("base64")
	}
	j.key("_links")
	j.raw("{}")
	j.end()
	j.raw("}")
	return j.Bytes()
}

func fileContentJSON(path, oid string, data []byte) []byte {
	name := path
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		name = path[i+1:]
	}
	j := newJSON()
	j.raw("{")
	j.begin()
	j.key("type")
	j.string("file")
	j.key("encoding")
	j.string("base64")
	j.key("size")
	j.int(len(data))
	j.key("name")
	j.string(name)
	j.key("path")
	j.string(path)
	j.key("content")
	j.string(base64Of(data))
	j.key("sha")
	j.string(oid)
	j.key("url")
	j.raw(`""`)
	j.key("html_url")
	j.raw(`""`)
	j.key("git_url")
	j.raw(`""`)
	j.key("download_url")
	j.raw(`""`)
	j.key("_links")
	j.raw("{}")
	j.end()
	j.raw("}")
	return j.Bytes()
}

func stripBase64Whitespace(s string) string {
	var out []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\r' || c == '\n' || c == '\t' || c == ' ' {
			continue
		}
		out = append(out, c)
	}
	return string(out)
}

func handleContents(f *forge, r *repo, rest []string, req *restRequest) *reply {
	if len(rest) == 0 {
		return notFoundReply()
	}
	// rest.ts joins the raw segments then decodes the whole sequence.
	filePath := decodePath(strings.Join(rest, "/"))
	ref := "refs/heads/" + r.defaultBranch
	if v, ok := req.query["ref"]; ok {
		ref = v
	}
	tip, _ := commitOfRef(r, ref)
	segments := splitPathSegments(filePath)

	if req.method == "GET" {
		entries := treeEntries(r, tip)
		var entry *treeEntry
		current := entries
		for index, segment := range segments {
			var found *treeEntry
			for i := range current {
				if current[i].name == segment {
					found = &current[i]
					break
				}
			}
			if found == nil {
				entry = nil
				break
			}
			if index == len(segments)-1 {
				entry = found
				break
			}
			if found.mode != "40000" {
				entry = nil
				break
			}
			current = treeEntriesOf(r, found.oid)
			entry = nil
		}
		if entry == nil {
			return notFoundReply()
		}
		if entry.mode == "40000" {
			listing := treeEntriesOf(r, entry.oid)
			j := newJSON()
			j.raw("[")
			for i, child := range listing {
				if i > 0 {
					j.raw(",")
				}
				childPath := child.name
				if filePath != "" {
					childPath = filePath + "/" + child.name
				}
				j.buf = append(j.buf, contentEntryJSON(r, childPath, child)...)
			}
			j.raw("]")
			return jsonReply(200, j.Bytes())
		}
		object, ok := r.objects[entry.oid]
		if !ok {
			return notFoundReply()
		}
		return jsonReply(200, fileContentJSON(filePath, entry.oid, object.data))
	}

	if req.method == "PUT" {
		body := asObjectBody(req.body)
		if body == nil {
			return problemsParsingReply()
		}
		message := body.chainMember("message").strV()
		if message == "" {
			message = "Update contents"
		}
		branchName := r.defaultBranch
		if v, ok := body.member("branch"); ok && v.kind == jsonString && v.strV() != "" {
			branchName = v.strV()
		}
		branchRef := "refs/heads/" + branchName
		content := stripBase64Whitespace(body.chainMember("content").strV())
		data, err := base64.StdEncoding.DecodeString(content)
		if err != nil {
			return jsonReply(422, messageErrorBytes(422, "content is not valid base64"))
		}
		tipSha, hasTip := commitOfRef(r, branchName)
		_, hasBranchRef := r.refs[branchRef]
		if !hasTip && !hasBranchRef {
			// An unborn branch: only the default branch may be born this way when
			// the repo already has commits.
			if len(r.refs) > 0 && branchName == r.defaultBranch {
				return jsonReply(422, messageErrorBytes(422, "branch "+branchName+" does not exist"))
			}
		}
		existingEntries := treeEntries(r, tipSha)
		existing, hasExisting := findEntryIn(r, existingEntries, segments)
		existingOid := ""
		if hasExisting && existing.mode != "40000" {
			existingOid = existing.oid
		}
		if sha, ok := body.member("sha"); ok && sha.kind == jsonString && sha.strV() != "" {
			if existingOid != "" && sha.strV() != existingOid {
				return jsonReply(409, messageErrorBytes(409, filePath+" does not match "+sha.strV()))
			}
		}
		blobOid := f.putObject(r, typeBlob, data)
		if existingOid == blobOid {
			if sha, ok := body.member("sha"); ok && sha.kind == jsonString && sha.strV() != "" {
				return jsonReply(422, messageErrorBytes(422, "the content is already at this path"))
			}
		}
		treeOid := setTreePath(f, r, existingEntries, segments, blobOid)
		commitSha := commitContents(f, r, tipSha, treeOid, message)
		r.refs[branchRef] = commitSha
		status := 200
		if existingOid == "" {
			status = 201
		}
		j := newJSON()
		j.raw("{")
		j.begin()
		j.key("content")
		j.raw("{")
		j.begin()
		j.key("name")
		j.string(segments[len(segments)-1])
		j.key("path")
		j.string(filePath)
		j.key("sha")
		j.string(blobOid)
		j.key("size")
		j.int(len(data))
		j.key("url")
		j.raw(`""`)
		j.key("html_url")
		j.raw(`""`)
		j.key("git_url")
		j.raw(`""`)
		j.key("download_url")
		j.raw(`""`)
		j.key("type")
		j.string("file")
		j.key("encoding")
		j.string("base64")
		j.key("content")
		j.string(base64Of(data))
		j.end()
		j.raw("}")
		j.key("commit")
		if view, ok := commitJSONBytes(f, r, commitSha); ok {
			j.buf = append(j.buf, view...)
		} else {
			j.null()
		}
		j.end()
		j.raw("}")
		return jsonReply(status, j.Bytes())
	}

	if req.method == "DELETE" {
		body := asObjectBody(req.body)
		if body == nil {
			return problemsParsingReply()
		}
		message := body.chainMember("message").strV()
		if message == "" {
			message = "Delete contents"
		}
		branchName := r.defaultBranch
		if v, ok := body.member("branch"); ok && v.kind == jsonString && v.strV() != "" {
			branchName = v.strV()
		}
		tipSha, hasTip := commitOfRef(r, branchName)
		if !hasTip {
			return notFoundReply()
		}
		existingEntries := treeEntries(r, tipSha)
		existing, hasExisting := findEntryIn(r, existingEntries, segments)
		if !hasExisting || existing.mode == "40000" {
			return notFoundReply()
		}
		if sha, ok := body.member("sha"); ok && sha.kind == jsonString && sha.strV() != "" && sha.strV() != existing.oid {
			return jsonReply(409, messageErrorBytes(409, filePath+" does not match "+sha.strV()))
		}
		treeOid := setTreePath(f, r, existingEntries, segments, "")
		commitSha := commitContents(f, r, tipSha, treeOid, message)
		r.refs["refs/heads/"+branchName] = commitSha
		j := newJSON()
		j.raw("{")
		j.begin()
		j.key("content")
		j.null()
		j.key("commit")
		if view, ok := commitJSONBytes(f, r, commitSha); ok {
			j.buf = append(j.buf, view...)
		} else {
			j.null()
		}
		j.end()
		j.raw("}")
		return jsonReply(200, j.Bytes())
	}

	return notFoundReply()
}

// ---------------------------------------------------------------- git data

func handleGitData(f *forge, r *repo, rest []string, req *restRequest) *reply {
	if len(rest) == 1 && rest[0] == "refs" && req.method == "POST" {
		body := asObjectBody(req.body)
		if body == nil {
			return problemsParsingReply()
		}
		ref := body.chainMember("ref").strV()
		sha := body.chainMember("sha").strV()
		if !strings.HasPrefix(ref, "refs/heads/") || !isObjectId(sha) {
			return jsonReply(422, messageErrorBytes(422, "Validation Failed"))
		}
		if _, exists := r.refs[ref]; exists {
			return jsonReply(422, messageErrorBytes(422, "Reference already exists"))
		}
		if _, hasObject := r.objects[sha]; !hasObject {
			return jsonReply(422, messageErrorBytes(422, "Object does not exist"))
		}
		r.refs[ref] = sha
		j := newJSON()
		j.raw("{")
		j.begin()
		j.key("ref")
		j.string(ref)
		j.key("node_id")
		j.string(nodeId("GitRef", strconv.Itoa(r.id)+":"+ref))
		j.key("url")
		j.raw(`""`)
		j.key("object")
		j.raw(`{"sha":`)
		j.string(sha)
		j.raw(`,"type":"commit","url":""}`)
		j.end()
		j.raw("}")
		return jsonReply(201, j.Bytes())
	}
	if len(rest) >= 2 && rest[0] == "refs" && req.method == "GET" {
		refName := decodePath(strings.Join(rest[1:], "/"))
		oid, ok := r.refs[refName]
		if !ok {
			return notFoundReply()
		}
		j := newJSON()
		j.raw("{")
		j.begin()
		j.key("ref")
		j.string(refName)
		j.key("node_id")
		j.raw(`""`)
		j.key("url")
		j.raw(`""`)
		j.key("object")
		j.raw(`{"sha":`)
		j.string(oid)
		j.raw(`,"type":"commit","url":""}`)
		j.end()
		j.raw("}")
		return jsonReply(200, j.Bytes())
	}
	return notFoundReply()
}

// ---------------------------------------------------------------- labels

func handleLabels(f *forge, r *repo, rest []string, req *restRequest) *reply {
	if len(rest) == 0 && req.method == "POST" {
		body := asObjectBody(req.body)
		if body == nil {
			return problemsParsingReply()
		}
		name := strings.TrimSpace(body.chainMember("name").strV())
		if name == "" {
			return jsonReply(422, messageErrorBytes(422, "Validation Failed"))
		}
		if existing := f.labelByName(r, name); existing != nil {
			return jsonReply(422, messageErrorBytes(422, "name already exists"))
		}
		color := body.chainMember("color").strV()
		if color == "" {
			color = "ededed"
		}
		l := f.createLabel(r, name, color)
		return jsonReply(201, labelJSONBytes(l))
	}
	if len(rest) == 0 && req.method == "GET" {
		labels := make([]*label, 0, len(r.labels))
		for _, l := range r.labels {
			labels = append(labels, l)
		}
		sort.Slice(labels, func(i, j int) bool { return labels[i].id < labels[j].id })
		j := newJSON()
		j.raw("[")
		for i, l := range labels {
			if i > 0 {
				j.raw(",")
			}
			j.buf = append(j.buf, labelJSONBytes(l)...)
		}
		j.raw("]")
		return jsonReply(200, j.Bytes())
	}
	return notFoundReply()
}

// ---------------------------------------------------------------- issues

func handleIssues(f *forge, r *repo, rest []string, req *restRequest) *reply {
	if len(rest) == 0 {
		if req.method == "POST" {
			return createIssueReply(f, r, req)
		}
		if req.method == "GET" {
			return listIssuesReply(f, r, req)
		}
		return notFoundReply()
	}
	number, err := strconv.Atoi(rest[0])
	if err != nil {
		return notFoundReply()
	}
	issueRec, hasIssue := r.issues[number]
	if len(rest) == 1 {
		if req.method != "GET" && req.method != "PATCH" {
			return notFoundReply()
		}
		if !hasIssue || issueRec.isPull {
			return notFoundReply()
		}
		if req.method == "GET" {
			return jsonReply(200, issueJSONBytes(f, r, issueRec))
		}
		return patchIssue(f, r, issueRec, req)
	}
	if rest[1] == "comments" && len(rest) == 2 {
		if !hasIssue {
			return notFoundReply()
		}
		if req.method == "POST" {
			body := asObjectBody(req.body)
			if body == nil {
				return problemsParsingReply()
			}
			comment := f.createComment(r, number, body.chainMember("body").strV(), f.viewerLogin)
			if comment == nil {
				return notFoundReply()
			}
			return jsonReply(201, commentJSONBytes(f, r, comment))
		}
		if req.method == "GET" {
			comments := f.commentsOf(r, number)
			j := newJSON()
			j.raw("[")
			for i, c := range comments {
				if i > 0 {
					j.raw(",")
				}
				j.buf = append(j.buf, commentJSONBytes(f, r, c)...)
			}
			j.raw("]")
			return jsonReply(200, j.Bytes())
		}
		return notFoundReply()
	}
	return notFoundReply()
}

func createIssueReply(f *forge, r *repo, req *restRequest) *reply {
	body := asObjectBody(req.body)
	if body == nil {
		return problemsParsingReply()
	}
	title := body.chainMember("title").strV()
	if title == "" {
		return jsonReply(422, messageErrorBytes(422, "Validation Failed"))
	}
	var labels []string
	if v, ok := body.member("labels"); ok && v.kind == jsonArray {
		for _, item := range v.items {
			if item.kind == jsonString && item.strV() != "" {
				labels = append(labels, item.strV())
			}
		}
	}
	for _, name := range labels {
		if f.labelByName(r, name) == nil {
			return jsonReply(422, messageErrorBytes(422, "Validation Failed: label \""+name+"\" does not exist"))
		}
	}
	issueRec := f.createIssue(r, title, body.chainMember("body").strV(), labels, f.viewerLogin, false, nil)
	return jsonReply(201, issueJSONBytes(f, r, issueRec))
}

func listIssuesReply(f *forge, r *repo, req *restRequest) *reply {
	state := "open"
	if v, ok := req.query["state"]; ok {
		state = v
	}
	sortKind := "created"
	if v, ok := req.query["sort"]; ok {
		sortKind = v
	}
	direction := "desc"
	if v, ok := req.query["direction"]; ok {
		direction = v
	}
	var issues []*issue
	for _, n := range r.issueOrder {
		is := r.issues[n]
		if is != nil && !is.isPull {
			issues = append(issues, is)
		}
	}
	if state == "open" || state == "closed" {
		var filtered []*issue
		for _, is := range issues {
			if is.state == state {
				filtered = append(filtered, is)
			}
		}
		issues = filtered
	}
	less := func(a, b *issue) bool {
		order := 0
		switch sortKind {
		case "created":
			if a.createdAt < b.createdAt {
				order = -1
			} else if a.createdAt > b.createdAt {
				order = 1
			}
		case "updated":
			if a.updatedAt < b.updatedAt {
				order = -1
			} else if a.updatedAt > b.updatedAt {
				order = 1
			}
		case "comments":
			order = a.number - b.number
		}
		if order == 0 {
			order = a.number - b.number
		}
		if direction == "asc" {
			return order < 0
		}
		return order > 0
	}
	sort.SliceStable(issues, func(i, j int) bool { return less(issues[i], issues[j]) })
	perPage := clampInt(req.query["per_page"], hasKey(req, "per_page"), 1, 100, 30)
	page := clampInt(req.query["page"], hasKey(req, "page"), 1, 1<<50, 1)
	start := (page - 1) * perPage
	end := start + perPage
	if start > len(issues) {
		start = len(issues)
	}
	if end > len(issues) {
		end = len(issues)
	}
	j := newJSON()
	j.raw("[")
	for i, is := range issues[start:end] {
		if i > 0 {
			j.raw(",")
		}
		j.buf = append(j.buf, issueJSONBytes(f, r, is)...)
	}
	j.raw("]")
	return jsonReply(200, j.Bytes())
}

func patchIssue(f *forge, r *repo, is *issue, req *restRequest) *reply {
	body := asObjectBody(req.body)
	if body == nil {
		return problemsParsingReply()
	}
	if v, ok := body.member("title"); ok && v.kind == jsonString {
		is.title = v.strV()
	}
	if v, ok := body.member("body"); ok && v.kind == jsonString {
		is.body = v.strV()
	}
	if v, ok := body.member("state"); ok && (v.strV() == "open" || v.strV() == "closed") {
		is.state = v.strV()
		if v.strV() == "closed" {
			iso := f.clock.isoString()
			is.closedAt = &iso
		} else {
			is.closedAt = nil
		}
	}
	is.updatedAt = f.clock.isoString()
	delete(r.issueReplyCache, is.number)
	f.invalidateIssue(r, is.number)
	return jsonReply(200, issueJSONBytes(f, r, is))
}

// ---------------------------------------------------------------- pulls

func pullFileViewJSON(file pullFile) []byte {
	j := newJSON()
	j.raw("{")
	j.begin()
	j.key("sha")
	j.string(file.sha)
	j.key("filename")
	j.string(file.filename)
	j.key("status")
	j.string(file.status)
	j.key("additions")
	j.int(file.additions)
	j.key("deletions")
	j.int(file.deletions)
	j.key("changes")
	j.int(file.changes)
	j.key("blob_url")
	j.raw(`""`)
	j.key("raw_url")
	j.raw(`""`)
	j.key("contents_url")
	j.raw(`""`)
	j.end()
	j.raw("}")
	return j.Bytes()
}

func handlePulls(f *forge, r *repo, rest []string, req *restRequest) *reply {
	if len(rest) == 0 {
		if req.method == "POST" {
			return createPullReply(f, r, req)
		}
		if req.method == "GET" {
			return listPullsReply(f, r, req)
		}
		return notFoundReply()
	}
	number, err := strconv.Atoi(rest[0])
	if err != nil {
		return notFoundReply()
	}
	p, hasPull := r.pulls[number]
	if len(rest) == 1 {
		if req.method != "GET" {
			return notFoundReply()
		}
		if !hasPull {
			return notFoundReply()
		}
		return jsonReply(200, pullJSONBytes(f, r, p))
	}
	if rest[1] == "files" && len(rest) == 2 && req.method == "GET" {
		if !hasPull {
			return notFoundReply()
		}
		j := newJSON()
		j.raw("[")
		for i, file := range p.files {
			if i > 0 {
				j.raw(",")
			}
			j.buf = append(j.buf, pullFileViewJSON(file)...)
		}
		j.raw("]")
		return jsonReply(200, j.Bytes())
	}
	if rest[1] == "merge" && len(rest) == 2 && req.method == "PUT" {
		return mergePullReply(f, r, req, number)
	}
	return notFoundReply()
}

func createPullReply(f *forge, r *repo, req *restRequest) *reply {
	body := asObjectBody(req.body)
	if body == nil {
		return problemsParsingReply()
	}
	head := body.chainMember("head").strV()
	base := r.defaultBranch
	if v, ok := body.member("base"); ok && v.kind == jsonString && v.strV() != "" {
		base = v.strV()
	}
	title := body.chainMember("title").strV()
	if head == "" || title == "" {
		return jsonReply(422, messageErrorBytes(422, "Validation Failed"))
	}
	headSha, hasHead := refTip(r, "refs/heads/"+head)
	baseSha, hasBase := refTip(r, "refs/heads/"+base)
	if !hasHead {
		return jsonReply(422, messageErrorBytes(422, "Validation Failed: head ref "+head+" does not exist"))
	}
	if !hasBase {
		return jsonReply(422, messageErrorBytes(422, "Validation Failed: base ref "+base+" does not exist"))
	}
	if headSha == baseSha {
		return jsonReply(422, messageErrorBytes(422, "No commits between base and head"))
	}
	for _, n := range r.pullOrder {
		existing := r.pulls[n]
		if existing != nil && existing.state == "open" && existing.head == head && existing.base == base {
			return jsonReply(422, messageErrorBytes(422, "A pull request already exists for "+head+"."))
		}
	}
	p := f.createPull(r, title, body.chainMember("body").strV(), head, base, headSha, baseSha, f.viewerLogin)
	p.files = f.diffFiles(r, baseSha, headSha)
	p.mergeable = "MERGEABLE"
	return jsonReply(201, pullJSONBytes(f, r, p))
}

func listPullsReply(f *forge, r *repo, req *restRequest) *reply {
	state := "open"
	if v, ok := req.query["state"]; ok {
		state = v
	}
	var pulls []*pull
	for _, n := range r.pullOrder {
		p := r.pulls[n]
		if p == nil {
			continue
		}
		if state == "open" {
			if p.state != "open" {
				continue
			}
		} else if state == "closed" {
			if p.state != "closed" {
				continue
			}
		}
		pulls = append(pulls, p)
	}
	sort.SliceStable(pulls, func(i, j int) bool { return pulls[i].number > pulls[j].number })
	perPage := clampInt(req.query["per_page"], hasKey(req, "per_page"), 1, 100, 30)
	page := clampInt(req.query["page"], hasKey(req, "page"), 1, 1<<50, 1)
	start := (page - 1) * perPage
	end := start + perPage
	if start > len(pulls) {
		start = len(pulls)
	}
	if end > len(pulls) {
		end = len(pulls)
	}
	j := newJSON()
	j.raw("[")
	for i, p := range pulls[start:end] {
		if i > 0 {
			j.raw(",")
		}
		j.buf = append(j.buf, pullJSONBytes(f, r, p)...)
	}
	j.raw("]")
	return jsonReply(200, j.Bytes())
}

// mergePull squash-merges a pull; answers null on success or the error message.
func mergePullInternal(f *forge, r *repo, p *pull) string {
	if p.merged {
		return "Pull Request is not mergeable: already merged"
	}
	if p.state != "open" {
		return "Pull Request is not open"
	}
	headTip, hasHead := refTip(r, "refs/heads/"+p.head)
	baseTip, hasBase := refTip(r, "refs/heads/"+p.base)
	if !hasHead || !hasBase {
		return "Pull Request is not mergeable: head or base ref is gone"
	}
	if baseTip != p.baseSha {
		headChanges := map[string]bool{}
		for _, file := range f.diffFiles(r, p.baseSha, headTip) {
			headChanges[file.filename] = true
		}
		if len(headChanges) > 0 {
			for _, file := range f.diffFiles(r, p.baseSha, baseTip) {
				if headChanges[file.filename] {
					p.mergeable = "CONFLICTING"
					return "Pull Request is not mergeable: merge conflict"
				}
			}
		}
	}
	headCommit, ok := r.objects[headTip]
	if !ok {
		return "Pull Request is not mergeable: head commit is unreadable"
	}
	c, err := parseCommit(headCommit.data)
	if err != nil {
		return "Pull Request is not mergeable: head commit is unreadable"
	}
	who := f.viewerLogin + " <" + f.viewerLogin + "@example.invalid>"
	var lines []string
	lines = append(lines, "tree "+c.tree)
	lines = append(lines, "parent "+baseTip)
	lines = append(lines, "author "+who+" "+strconv.FormatInt(f.clock.nowSec(), 10)+" +0000")
	lines = append(lines, "committer "+who+" "+strconv.FormatInt(f.clock.nowSec(), 10)+" +0000")
	lines = append(lines, "")
	lines = append(lines, p.title+" (#"+strconv.Itoa(p.number)+")\n")
	data := []byte(strings.Join(lines, "\n"))
	mergeSha := f.putObject(r, typeCommit, data)
	r.refs["refs/heads/"+p.base] = mergeSha
	p.merged = true
	p.state = "closed"
	iso := f.clock.isoString()
	p.mergedAt = &iso
	p.mergeCommitSha = &mergeSha
	p.updatedAt = iso
	p.issue.state = "closed"
	p.issue.closedAt = &iso
	delete(r.issueReplyCache, p.issue.number)
	f.invalidateIssue(r, p.issue.number)
	r.clearGitCaches()
	return ""
}

func mergePullReply(f *forge, r *repo, req *restRequest, number int) *reply {
	p, ok := r.pulls[number]
	if !ok {
		return notFoundReply()
	}
	if p.merged {
		return jsonReply(405, messageErrorBytes(405, "Pull Request is not mergeable: already merged"))
	}
	if p.state != "open" {
		return jsonReply(405, messageErrorBytes(405, "Pull Request is not open"))
	}
	headTip, hasHead := refTip(r, "refs/heads/"+p.head)
	if !hasHead {
		return jsonReply(405, messageErrorBytes(405, "Pull Request is not mergeable: head ref is gone"))
	}
	baseTip, hasBase := refTip(r, "refs/heads/"+p.base)
	if !hasBase {
		return jsonReply(405, messageErrorBytes(405, "Pull Request is not mergeable: base ref is gone"))
	}
	// A conflict: the base moved and touched a path the head also changed.
	if baseTip != p.baseSha {
		headChanges := map[string]bool{}
		for _, file := range f.diffFiles(r, p.baseSha, headTip) {
			headChanges[file.filename] = true
		}
		if len(headChanges) > 0 {
			for _, file := range f.diffFiles(r, baseTip, p.baseSha) {
				if headChanges[file.filename] {
					p.mergeable = "CONFLICTING"
					return jsonReply(405, messageErrorBytes(405, "Pull Request is not mergeable: merge conflict"))
				}
			}
		}
	}
	headCommit, ok := r.objects[headTip]
	if !ok || headCommit.typ != typeCommit {
		return jsonReply(405, messageErrorBytes(405, "Pull Request is not mergeable: head tree is unreadable"))
	}
	c, err := parseCommit(headCommit.data)
	if err != nil {
		return jsonReply(405, messageErrorBytes(405, "Pull Request is not mergeable: head tree is unreadable"))
	}
	who := f.viewerLogin + " <" + f.viewerLogin + "@example.invalid>"
	var lines []string
	lines = append(lines, "tree "+c.tree)
	lines = append(lines, "parent "+baseTip)
	lines = append(lines, "author "+who+" "+strconv.FormatInt(f.clock.nowSec(), 10)+" +0000")
	lines = append(lines, "committer "+who+" "+strconv.FormatInt(f.clock.nowSec(), 10)+" +0000")
	lines = append(lines, "")
	lines = append(lines, p.title+" (#"+strconv.Itoa(p.number)+")\n")
	data := []byte(strings.Join(lines, "\n"))
	mergeSha := f.putObject(r, typeCommit, data)
	r.refs["refs/heads/"+p.base] = mergeSha
	p.merged = true
	p.state = "closed"
	iso := f.clock.isoString()
	p.mergedAt = &iso
	p.mergeCommitSha = &mergeSha
	p.updatedAt = iso
	p.issue.state = "closed"
	p.issue.closedAt = &iso
	delete(r.issueReplyCache, p.issue.number)
	f.invalidateIssue(r, p.issue.number)
	r.clearGitCaches()
	j := newJSON()
	j.raw("{")
	j.begin()
	j.key("sha")
	j.string(mergeSha)
	j.key("merged")
	j.boolean(true)
	j.key("message")
	j.string("Pull Request successfully merged")
	j.end()
	j.raw("}")
	return jsonReply(200, j.Bytes())
}
