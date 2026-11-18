// GraphQL v4 resolvers: Query, Mutation and the object types over the store
// (graphql.ts's resolveField and resolveMutation), and the endpoint.
package main

import (
	"regexp"
	"strconv"
	"strings"
)

// gqlResolveField answers one field's value against its type and source:
// gqlMissing when the field is not on the type; a *gqlNode for a nested
// selection; a []byte for a leaf's serialized bytes; nil for a null.
func gqlResolveField(ctx *gqlContext, typeName string, source any, field gqlSelection, path []string) any {
	f := ctx.forge
	variables := ctx.variables
	child := field.selection
	ip := func() []string { return innerPath(path, field) }

	switch typeName {
	case "Query":
		return gqlResolveQuery(ctx, field, ip())

	case "Mutation":
		return gqlResolveMutation(ctx, field, ip())

	case "Repository":
		r := source.(*repo)
		switch field.name {
		case "id":
			return appendJSONString(nil, globalId("Repository", strconv.Itoa(r.id)))
		case "databaseId":
			return []byte(strconv.Itoa(r.id))
		case "name":
			return appendJSONString(nil, r.name)
		case "nameWithOwner":
			return appendJSONString(nil, r.owner+"/"+r.name)
		case "owner":
			var u *user
			if got, ok := f.user(r.owner); ok {
				u = got
			} else {
				u = &user{login: r.owner}
			}
			return gqlExecute(ctx, "User", u, child, ip())
		case "defaultBranchRef":
			oid, ok := r.refs["refs/heads/"+r.defaultBranch]
			if !ok {
				return []byte("null")
			}
			return gqlExecute(ctx, "Ref", &gqlRefSource{name: r.defaultBranch, oid: oid}, child, ip())
		case "viewerPermission":
			if r.owner == f.viewerLogin {
				return []byte(`"ADMIN"`)
			}
			return []byte(`"READ"`)
		case "hasIssuesEnabled":
			return []byte("true")
		case "issue":
			number, ok := gqlArgOf(field, "number", variables)
			if !ok || number.kind != jsonNumber {
				return []byte("null")
			}
			issueRec := r.issues[int(number.num)]
			if issueRec == nil || issueRec.isPull {
				return []byte("null")
			}
			return gqlExecute(ctx, "Issue", issueRec, child, ip())
		case "issues":
			nodes := gqlIssuesOf(f, r, field, variables)
			anyNodes := make([]any, len(nodes))
			for i, n := range nodes {
				anyNodes[i] = n
			}
			return gqlConnectionResult(ctx, "Issue", anyNodes, field, variables, child, ip())
		case "pullRequest":
			number, ok := gqlArgOf(field, "number", variables)
			if !ok || number.kind != jsonNumber {
				return []byte("null")
			}
			p := r.pulls[int(number.num)]
			if p == nil {
				return []byte("null")
			}
			return gqlExecute(ctx, "PullRequest", p, child, ip())
		case "pullRequests":
			var nodes []*pull
			for _, n := range r.pullOrder {
				if p := r.pulls[n]; p != nil {
					nodes = append(nodes, p)
				}
			}
			// sort by number desc
			for i := 1; i < len(nodes); i++ {
				for j := i; j > 0 && nodes[j].number > nodes[j-1].number; j-- {
					nodes[j], nodes[j-1] = nodes[j-1], nodes[j]
				}
			}
			anyNodes := make([]any, len(nodes))
			for i, n := range nodes {
				anyNodes[i] = n
			}
			return gqlConnectionResult(ctx, "PullRequest", anyNodes, field, variables, child, ip())
		}
		return gqlMissing

	case "User":
		u := source.(*user)
		switch field.name {
		case "login":
			return appendJSONString(nil, u.login)
		case "id":
			return appendJSONString(nil, globalId("User", u.login))
		}
		return gqlMissing

	case "Ref":
		ref := source.(*gqlRefSource)
		switch field.name {
		case "name":
			return appendJSONString(nil, ref.name)
		case "id":
			return appendJSONString(nil, globalId("Ref", ref.name))
		case "target":
			r := f.currentRepo()
			if r == nil {
				return []byte("null")
			}
			object, ok := r.objects[ref.oid]
			if !ok {
				return []byte("null")
			}
			if object.typ == typeCommit {
				return gqlExecute(ctx, "Commit", ref.oid, child, ip())
			}
			if object.typ == typeTag {
				return gqlExecute(ctx, "Tag", ref.oid, child, ip())
			}
			return []byte("null")
		}
		return gqlMissing

	case "Commit":
		sha := source.(string)
		r := f.currentRepo()
		if r == nil {
			return []byte("null")
		}
		object, ok := r.objects[sha]
		if !ok {
			return []byte("null")
		}
		switch field.name {
		case "id":
			return appendJSONString(nil, globalId("Commit", sha))
		case "oid":
			return appendJSONString(nil, sha)
		case "message":
			return appendJSONString(nil, messageOfCommit(string(object.data)))
		case "committedDate", "pushedDate", "authoredDate":
			return appendJSONString(nil, rfc3339FromMs(1_735_689_600_000))
		}
		return gqlMissing

	case "Tag":
		sha := source.(string)
		r := f.currentRepo()
		if r == nil {
			return []byte("null")
		}
		object, ok := r.objects[sha]
		if !ok {
			return []byte("null")
		}
		switch field.name {
		case "id":
			return appendJSONString(nil, globalId("Tag", sha))
		case "oid":
			return appendJSONString(nil, sha)
		case "message":
			return appendJSONString(nil, messageOfCommit(string(object.data)))
		}
		return gqlMissing

	case "Issue":
		is := source.(*issue)
		switch field.name {
		case "id":
			return appendJSONString(nil, globalId("Issue", strconv.Itoa(is.repoID)+"I"+strconv.Itoa(is.number)))
		case "number":
			return []byte(strconv.Itoa(is.number))
		case "title":
			return appendJSONString(nil, is.title)
		case "state":
			if is.state == "closed" {
				return []byte(`"CLOSED"`)
			}
			return []byte(`"OPEN"`)
		case "body":
			return appendJSONString(nil, is.body)
		case "createdAt":
			return appendJSONString(nil, is.createdAt)
		case "updatedAt":
			return appendJSONString(nil, is.updatedAt)
		}
		return gqlMissing

	case "PullRequest":
		p := source.(*pull)
		switch field.name {
		case "id":
			return appendJSONString(nil, globalId("PullRequest", strconv.Itoa(p.repoID)+"P"+strconv.Itoa(p.number)))
		case "number":
			return []byte(strconv.Itoa(p.number))
		case "title":
			return appendJSONString(nil, p.title)
		case "state":
			if p.merged {
				return []byte(`"MERGED"`)
			}
			if p.state == "closed" {
				return []byte(`"CLOSED"`)
			}
			return []byte(`"OPEN"`)
		case "merged":
			return []byte(strconv.FormatBool(p.merged))
		case "headRefName":
			return appendJSONString(nil, p.head)
		case "baseRefName":
			return appendJSONString(nil, p.base)
		case "mergeable":
			return appendJSONString(nil, p.mergeable)
		case "files":
			files := p.files
			first, ok := gqlArgNumber(field, "first", variables)
			limit := len(files)
			if ok && first > 0 && int(first) < limit {
				limit = int(first)
			}
			page := files[:limit]
			nodesSel := childSelection(child, "nodes")
			nodesBuf := make([]byte, 0, 64)
			nodesBuf = append(nodesBuf, '[')
			for i, file := range page {
				if i > 0 {
					nodesBuf = append(nodesBuf, ',')
				}
				n := gqlExecute(ctx, "PullRequestChangedFile", &gqlChangedFile{path: file.filename}, nodesSel, append(append(append([]string{}, ip()...), "nodes"), strconv.Itoa(i)))
				nodesBuf = append(nodesBuf, n.bytes()...)
			}
			nodesBuf = append(nodesBuf, ']')
			obj := &gqlNode{}
			obj.emit("nodes", nodesBuf)
			pageInfo := &gqlNode{}
			pageInfo.emit("hasNextPage", []byte("false"))
			pageInfo.emit("endCursor", []byte("null"))
			obj.emitNode("pageInfo", pageInfo)
			obj.emit("totalCount", []byte(strconv.Itoa(len(files))))
			return obj
		}
		return gqlMissing

	case "PullRequestChangedFile":
		file := source.(*gqlChangedFile)
		switch field.name {
		case "path":
			return appendJSONString(nil, file.path)
		}
		return gqlMissing

	case "IssueComment":
		comment := source.(*comment)
		switch field.name {
		case "id":
			return appendJSONString(nil, globalId("IssueComment", strconv.Itoa(comment.id)))
		case "body":
			return appendJSONString(nil, comment.body)
		case "createdAt":
			return appendJSONString(nil, comment.createdAt)
		case "updatedAt":
			return appendJSONString(nil, comment.createdAt)
		case "author":
			return gqlExecute(ctx, "User", &user{login: comment.user}, child, ip())
		}
		return gqlMissing

	case "__Type":
		t := source.(*introspectTypeDef)
		switch field.name {
		case "name":
			return appendJSONString(nil, t.name)
		case "kind":
			return appendJSONString(nil, t.kind)
		case "fields":
			if t.fields == nil {
				return []byte("null")
			}
			includeDeprecated := false
			if v, ok := gqlArgOf(field, "includeDeprecated", variables); ok && v.kind == jsonBool {
				includeDeprecated = v.boolean
			}
			var names []string
			if t.fieldOrder != nil {
				names = append(names, t.fieldOrder...)
			} else {
				for name := range t.fields {
					names = append(names, name)
				}
			}
			obj := &gqlNode{}
			arr := make([]byte, 0, 64)
			arr = append(arr, '[')
			count := 0
			for _, name := range names {
				if !includeDeprecated && strings.HasPrefix(name, "_deprecated") {
					continue
				}
				if count > 0 {
					arr = append(arr, ',')
				}
				count++
				inner := gqlExecute(ctx, "__Field", &gqlNamedField{name: name}, child, append(append([]string{}, ip()...), name))
				arr = append(arr, inner.bytes()...)
			}
			arr = append(arr, ']')
			obj.emit(keyAlias(field), arr)
			return obj
		case "enumValues":
			if t.enumValues == nil {
				return []byte("null")
			}
			includeDeprecated := false
			if v, ok := gqlArgOf(field, "includeDeprecated", variables); ok && v.kind == jsonBool {
				includeDeprecated = v.boolean
			}
			obj := &gqlNode{}
			arr := make([]byte, 0, 64)
			arr = append(arr, '[')
			count := 0
			for _, value := range t.enumValues {
				if !includeDeprecated && strings.HasPrefix(value, "_") {
					continue
				}
				if count > 0 {
					arr = append(arr, ',')
				}
				count++
				inner := gqlExecute(ctx, "__EnumValue", &gqlNamedField{name: value}, child, append(append([]string{}, ip()...), value))
				arr = append(arr, inner.bytes()...)
			}
			arr = append(arr, ']')
			obj.emit(keyAlias(field), arr)
			return obj
		}
		return gqlMissing

	case "__Field":
		named := source.(*gqlNamedField)
		switch field.name {
		case "name":
			return appendJSONString(nil, named.name)
		}
		return gqlMissing

	case "__EnumValue":
		named := source.(*gqlNamedField)
		switch field.name {
		case "name":
			return appendJSONString(nil, named.name)
		}
		return gqlMissing

	case "__Schema":
		switch field.name {
		case "queryType":
			return gqlExecute(ctx, "__Type", introspectType("Query"), child, ip())
		case "types":
			return []byte("[]")
		}
		return gqlMissing
	}
	return gqlMissing
}

// keyAlias answers a field's output key.
func keyAlias(field gqlSelection) string {
	if field.hasAlias {
		return field.alias
	}
	return field.name
}

type gqlRefSource struct {
	name string
	oid  string
}

type gqlChangedFile struct {
	path string
}

type gqlNamedField struct {
	name string
}

// gqlIssuesOf answers the issues a repository.issues field selects.
func gqlIssuesOf(f *forge, r *repo, field gqlSelection, variables map[string]jsonValue) []*issue {
	var issues []*issue
	for _, n := range r.issueOrder {
		if is := r.issues[n]; is != nil && !is.isPull {
			issues = append(issues, is)
		}
	}
	if states, ok := gqlArgOf(field, "states", variables); ok && states.kind == jsonArray && len(states.items) > 0 {
		wanted := map[string]bool{}
		for _, s := range states.items {
			if s.kind == jsonString {
				if s.str == "OPEN" {
					wanted["open"] = true
				} else if s.str == "CLOSED" {
					wanted["closed"] = true
				}
			}
		}
		var filtered []*issue
		for _, is := range issues {
			if wanted[is.state] {
				filtered = append(filtered, is)
			}
		}
		issues = filtered
	}
	orderField := "created"
	direction := "desc"
	if orderBy, ok := gqlArgOf(field, "orderBy", variables); ok && orderBy.kind == jsonObject {
		if v, hasField := orderBy.member("field"); hasField && v.kind == jsonString {
			if v.strV() == "UPDATED_AT" {
				orderField = "updated"
			} else if v.strV() == "COMMENTS" {
				orderField = "comments"
			}
		}
		if v, hasDir := orderBy.member("direction"); hasDir && v.kind == jsonString && v.strV() == "ASC" {
			direction = "asc"
		}
	}
	less := func(a, b *issue) bool {
		order := 0
		switch orderField {
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
		default:
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
	// stable insertion sort (a handful of issues)
	for i := 1; i < len(issues); i++ {
		for j := i; j > 0 && less(issues[j], issues[j-1]); j-- {
			issues[j], issues[j-1] = issues[j-1], issues[j]
		}
	}
	return issues
}

// connectionCursor encodes one: base64("cursor:N").
func connectionCursor(number int) string {
	return encodeBase64String("cursor:" + strconv.Itoa(number))
}

func encodeBase64String(s string) string {
	return base64Encode([]byte(s))
}

var cursorDecodeRe = regexp.MustCompile(`^cursor:(.*)$`)

func decodeConnectionCursor(cursor string) (string, bool) {
	text, err := base64DecodeString(cursor)
	if err != nil {
		return "", false
	}
	m := cursorDecodeRe.FindSubmatch(text)
	if m == nil {
		return "", false
	}
	return string(m[1]), true
}

// gqlConnectionResult answers a connection: nodes, pageInfo, totalCount.
func gqlConnectionResult(ctx *gqlContext, typeName string, nodes []any, field gqlSelection, variables map[string]jsonValue, child []gqlSelection, path []string) any {
	first, hasFirst := gqlArgNumber(field, "first", variables)
	after, hasAfter := gqlArgStr(field, "after", variables)
	start := 0
	if hasAfter && after != "" {
		if decoded, ok := decodeConnectionCursor(after); ok {
			start = -1
			for i, node := range nodes {
				if gqlNumberOf(node) == decoded {
					start = i
					break
				}
			}
			if start < 0 {
				start = 0
			} else {
				start++
			}
		}
	}
	limit := len(nodes)
	if hasFirst && first > 0 && int(first) < limit {
		limit = int(first)
	}
	end := start + limit
	if end > len(nodes) {
		end = len(nodes)
	}
	if start > len(nodes) {
		start = len(nodes)
	}
	if end < start {
		end = start
	}
	page := nodes[start:end]
	var last *int
	if len(page) > 0 {
		n := gqlNumberOf(page[len(page)-1])
		if n != "" {
			num, err := strconv.Atoi(n)
			if err == nil {
				last = &num
			}
		}
	}
	hasNextPage := start+len(page) < len(nodes)
	nodesSel := childSelection(child, "nodes")
	pageInfoSel := childSelection(child, "pageInfo")

	nodesBuf := make([]byte, 0, 64)
	nodesBuf = append(nodesBuf, '[')
	for i, node := range page {
		if i > 0 {
			nodesBuf = append(nodesBuf, ',')
		}
		inner := gqlExecute(ctx, typeName, node, nodesSel, append(append(append([]string{}, path...), keyAlias(field)), "nodes", strconv.Itoa(i)))
		nodesBuf = append(nodesBuf, inner.bytes()...)
	}
	nodesBuf = append(nodesBuf, ']')

	obj := &gqlNode{}
	obj.emit("nodes", nodesBuf)
	pageInfo := gqlExecutePageInfo(pageInfoSel, hasNextPage, last)
	obj.emitNode("pageInfo", pageInfo)
	obj.emit("totalCount", []byte(strconv.Itoa(len(nodes))))
	return obj
}

// gqlExecutePageInfo answers a pageInfo selection.
func gqlExecutePageInfo(selection []gqlSelection, hasNextPage bool, endCursor *int) *gqlNode {
	node := &gqlNode{}
	for _, sel := range selection {
		if sel.kind != 0 {
			continue
		}
		key := keyAlias(sel)
		switch sel.name {
		case "hasNextPage":
			node.emit(key, []byte(strconv.FormatBool(hasNextPage)))
		case "endCursor":
			if hasNextPage && endCursor != nil {
				node.emit(key, appendJSONString(nil, connectionCursor(*endCursor)))
			} else if endCursor != nil {
				node.emit(key, appendJSONString(nil, connectionCursor(*endCursor)))
			} else {
				node.emit(key, []byte("null"))
			}
		case "startCursor":
			node.emit(key, []byte("null"))
		case "hasPreviousPage":
			node.emit(key, []byte("false"))
		}
	}
	return node
}

func gqlNumberOf(node any) string {
	switch n := node.(type) {
	case *issue:
		return strconv.Itoa(n.number)
	case *pull:
		return strconv.Itoa(n.number)
	}
	return ""
}

// ---------------------------------------------------------------- mutations

func gqlResolveMutation(ctx *gqlContext, field gqlSelection, path []string) any {
	f := ctx.forge
	child := field.selection
	inputValue, hasInput := gqlArgOf(field, "input", ctx.variables)

	inputRecord := func() map[string]jsonValue {
		if !hasInput || inputValue.kind != jsonObject {
			ctx.errors = append(ctx.errors, gqlErrorEntry{message: `Argument "input" on InputObject "` + field.name + `" has invalid value.`})
			return nil
		}
		return inputValue.memberMap()
	}

	repoOf := func(record map[string]jsonValue) *repo {
		if record == nil {
			return nil
		}
		v, ok := record["repositoryId"]
		if !ok || v.kind != jsonString {
			return nil
		}
		typeName, key, ok := decodeGlobalId(v.str)
		if !ok || typeName != "Repository" {
			return nil
		}
		return f.repoByIDString(key)
	}

	switch field.name {
	case "createIssue":
		record := inputRecord()
		if record == nil {
			return []byte("null")
		}
		r := repoOf(record)
		if r == nil {
			ctx.errors = append(ctx.errors, gqlErrorEntry{message: "Could not resolve to a node with the global id of the repository"})
			return []byte("null")
		}
		title := recordMember(record, "title").strV()
		body := recordMember(record, "body").strV()
		issueRec := f.createIssue(r, title, body, []string{}, f.viewerLogin, false, nil)
		return gqlMutationPayload(ctx, "CreateIssuePayload", field, child, path, []gqlPayloadSource{{name: "issue", typeName: "Issue", source: issueRec}})
	case "closeIssue":
		record := inputRecord()
		if record == nil {
			return []byte("null")
		}
		node := gqlIssueOfId(f, recordMemberString(record, "issueId"))
		if node == nil {
			ctx.errors = append(ctx.errors, gqlErrorEntry{message: "Could not resolve to a node with the global id of the issue"})
			return []byte("null")
		}
		node.issue.state = "closed"
		iso := f.clock.isoString()
		node.issue.closedAt = &iso
		node.issue.updatedAt = iso
		delete(node.repo.issueReplyCache, node.issue.number)
		f.invalidateIssue(node.repo, node.issue.number)
		return gqlMutationPayload(ctx, "CloseIssuePayload", field, child, path, []gqlPayloadSource{{name: "issue", typeName: "Issue", source: node.issue}})
	case "createPullRequest":
		record := inputRecord()
		if record == nil {
			return []byte("null")
		}
		r := repoOf(record)
		if r == nil {
			ctx.errors = append(ctx.errors, gqlErrorEntry{message: "Could not resolve to a node with the global id of the repository"})
			return []byte("null")
		}
		head := recordMemberString(record, "headRefName")
		base := r.defaultBranch
		if v := recordMemberString(record, "baseRefName"); v != "" {
			base = v
		}
		title := recordMemberString(record, "title")
		headSha, hasHead := refTip(r, "refs/heads/"+head)
		baseSha, hasBase := refTip(r, "refs/heads/"+base)
		if !hasHead || !hasBase {
			ctx.errors = append(ctx.errors, gqlErrorEntry{message: "Head ref or base ref could not be resolved"})
			return []byte("null")
		}
		p := f.createPull(r, title, recordMemberString(record, "body"), head, base, headSha, baseSha, f.viewerLogin)
		p.files = f.diffFiles(r, baseSha, headSha)
		return gqlMutationPayload(ctx, "CreatePullRequestPayload", field, child, path, []gqlPayloadSource{{name: "pullRequest", typeName: "PullRequest", source: p}})
	case "mergePullRequest":
		record := inputRecord()
		if record == nil {
			return []byte("null")
		}
		node := gqlPullOfId(f, recordMemberString(record, "pullRequestId"))
		if node == nil {
			ctx.errors = append(ctx.errors, gqlErrorEntry{message: "Could not resolve to a node with the global id of the pull request"})
			return []byte("null")
		}
		if err := mergePullInternal(f, node.repo, node.pull); err != "" {
			ctx.errors = append(ctx.errors, gqlErrorEntry{message: err})
			return []byte("null")
		}
		return gqlMutationPayload(ctx, "MergePullRequestPayload", field, child, path, []gqlPayloadSource{{name: "pullRequest", typeName: "PullRequest", source: node.pull}})
	case "addComment":
		record := inputRecord()
		if record == nil {
			return []byte("null")
		}
		node := gqlIssueOfId(f, recordMemberString(record, "subjectId"))
		if node == nil {
			ctx.errors = append(ctx.errors, gqlErrorEntry{message: "Could not resolve to a node with the global id of the subject"})
			return []byte("null")
		}
		comment := f.createComment(node.repo, node.issue.number, recordMemberString(record, "body"), f.viewerLogin)
		if comment == nil {
			return []byte("null")
		}
		return gqlMutationPayload(ctx, "AddCommentPayload", field, child, path, []gqlPayloadSource{{name: "commentEdge", typeName: "", source: comment}})
	}
	return gqlMissing
}

func recordMember(record map[string]jsonValue, key string) jsonValue {
	if v, ok := record[key]; ok {
		return v
	}
	return jsonValue{}
}

func recordMemberString(record map[string]jsonValue, key string) string {
	v, ok := record[key]
	if ok && v.kind == jsonString {
		return v.str
	}
	return ""
}

// memberMap answers a jsonObject's members as a map (paired with memberOrder).
func (v jsonValue) memberMap() map[string]jsonValue {
	out := make(map[string]jsonValue, len(v.members))
	for _, m := range v.members {
		out[m.key] = m.value
	}
	return out
}

type gqlPayloadSource struct {
	name     string
	typeName string
	source   any
}

type gqlNodeRef struct {
	repo *repo
	issue *issue
}

type gqlPullRef struct {
	repo *repo
	pull *pull
}

func gqlIssueOfId(f *forge, id string) *gqlNodeRef {
	if id == "" {
		return nil
	}
	typeName, key, ok := decodeGlobalId(id)
	if !ok || typeName != "Issue" {
		return nil
	}
	m := gqlKeyIRe.FindStringSubmatch(key)
	if m == nil {
		return nil
	}
	r := f.repoByIDString(m[1])
	if r == nil {
		return nil
	}
	number, err := strconv.Atoi(m[2])
	if err != nil {
		return nil
	}
	issueRec, ok := r.issues[number]
	if !ok {
		return nil
	}
	return &gqlNodeRef{repo: r, issue: issueRec}
}

func gqlPullOfId(f *forge, id string) *gqlPullRef {
	if id == "" {
		return nil
	}
	typeName, key, ok := decodeGlobalId(id)
	if !ok || typeName != "PullRequest" {
		return nil
	}
	m := gqlKeyPRe.FindStringSubmatch(key)
	if m == nil {
		return nil
	}
	r := f.repoByIDString(m[1])
	if r == nil {
		return nil
	}
	number, err := strconv.Atoi(m[2])
	if err != nil {
		return nil
	}
	p, ok := r.pulls[number]
	if !ok {
		return nil
	}
	return &gqlPullRef{repo: r, pull: p}
}

// gqlMutationPayload answers a mutation's payload selection.
func gqlMutationPayload(ctx *gqlContext, typeName string, field gqlSelection, child []gqlSelection, path []string, sources []gqlPayloadSource) any {
	obj := &gqlNode{}
	for _, node := range child {
		if node.kind != 0 {
			continue
		}
		key := keyAlias(node)
		var source *gqlPayloadSource
		for i := range sources {
			if sources[i].name == node.name {
				source = &sources[i]
				break
			}
		}
		if source == nil {
			entry := gqlErrorEntry{message: `Cannot query field "` + node.name + `" on type "` + typeName + `".`}
			entry.path = append(append([]string{}, path...), key)
			ctx.errors = append(ctx.errors, entry)
			// graphql.ts's mutationPayload answers null for an unknown payload key
			obj.emit(key, []byte("null"))
			continue
		}
		switch node.name {
		case "issue":
			inner := gqlExecute(ctx, "Issue", source.source, node.selection, append(append([]string{}, path...), key))
			obj.emitNode(key, inner)
		case "pullRequest":
			inner := gqlExecute(ctx, "PullRequest", source.source, node.selection, append(append([]string{}, path...), key))
			obj.emitNode(key, inner)
		case "commentEdge":
			comment := source.source.(*comment)
			nodeSel := childSelection(node.selection, "node")
			edge := &gqlNode{}
			inner := gqlExecute(ctx, "IssueComment", comment, nodeSel, append(append(append([]string{}, path...), key), "node"))
			edge.emitNode("node", inner)
			obj.emitNode(key, edge)
		default:
			obj.emit(key, []byte("null"))
		}
	}
	return obj
}

// ---------------------------------------------------------------- the endpoint

type gqlParsedRequest struct {
	query        string
	variables    map[string]jsonValue
	operationName string
	hasOpName    bool
}

func gqlParseRequest(body jsonValue) *gqlParsedRequest {
	if body.kind != jsonObject {
		return nil
	}
	qv, hasQuery := body.member("query")
	if !hasQuery || qv.kind != jsonString {
		return nil
	}
	out := &gqlParsedRequest{
		query:     qv.str,
		variables: map[string]jsonValue{},
	}
	if v, ok := body.member("variables"); ok && v.kind == jsonObject {
		for _, m := range v.members {
			out.variables[m.key] = m.value
		}
	}
	if v, ok := body.member("operationName"); ok && v.kind == jsonString {
		out.operationName = v.str
		out.hasOpName = true
	}
	return out
}

func gqlOperationBody(f *forge, request *gqlParsedRequest) []byte {
	var document gqlDocument
	failed := func() (failed bool) {
		defer func() {
			if r := recover(); r != nil {
				failed = true
				if err, ok := r.(error); ok {
					parseErrorBody = []byte(`{"errors":[{"message":` + string(appendJSONString(nil, err.Error())) + `}]}`)
				} else {
					parseErrorBody = []byte(`{"errors":[{"message":"Syntax Error"}]}`)
				}
			}
		}()
		document = (&gqlParser{tokens: gqlTokenize(request.query)}).document()
		return false
	}()
	if failed {
		return parseErrorBody
	}
	operation := document.operations[0]
	if request.hasOpName {
		for _, candidate := range document.operations {
			if candidate.name == request.operationName {
				operation = candidate
				break
			}
		}
	}
	ctx := &gqlContext{forge: f, variables: request.variables, fragments: document.fragments}
	typeName := "Query"
	if operation.isMutation {
		typeName = "Mutation"
	}
	data := gqlExecute(ctx, typeName, nil, operation.selection, []string{})
	// The body: {"data":...} or {"data":...,"errors":[...]}
	if len(ctx.errors) == 0 {
		return []byte(`{"data":` + string(data.bytes()) + `}`)
	}
	errs := make([]byte, 0, 128)
	errs = append(errs, '[')
	for i, err := range ctx.errors {
		if i > 0 {
			errs = append(errs, ',')
		}
		errs = append(errs, '{')
		errs = append(errs, `"message":`...)
		errs = append(errs, appendJSONString(nil, err.message)...)
		if len(err.path) > 0 {
			errs = append(errs, `,"path":[`...)
			for j, p := range err.path {
				if j > 0 {
					errs = append(errs, ',')
				}
				errs = append(errs, appendJSONString(nil, p)...)
			}
			errs = append(errs, ']')
		}
		errs = append(errs, '}')
	}
	errs = append(errs, ']')
	return []byte(`{"data":` + string(data.bytes()) + `,"errors":` + string(errs) + `}`)
}

// gqlResolveQuery answers the Query type's fields.
func gqlResolveQuery(ctx *gqlContext, field gqlSelection, path []string) any {
	f := ctx.forge
	child := field.selection
	switch field.name {
	case "viewer":
		var u *user
		if got, ok := f.user(f.viewerLogin); ok {
			u = got
		} else {
			u = &user{login: f.viewerLogin}
		}
		return gqlExecute(ctx, "User", u, child, path)
	case "repository":
		r := gqlRepoArg(f, field, ctx.variables)
		if r == nil {
			return []byte("null")
		}
		return gqlExecute(ctx, "Repository", r, child, path)
	case "node":
		id, ok := gqlArgStr(field, "id", ctx.variables)
		if !ok {
			return []byte("null")
		}
		nodeType, nodeKey, ok := decodeGlobalId(id)
		if !ok {
			return []byte("null")
		}
		if nodeType == "Repository" {
			r := f.repoByIDString(nodeKey)
			if r == nil {
				return []byte("null")
			}
			return gqlExecute(ctx, "Repository", r, child, path)
		}
		return []byte("null")
	case "__type":
		name, ok := gqlArgStr(field, "name", ctx.variables)
		if !ok {
			return []byte("null")
		}
		t := introspectType(name)
		if t == nil {
			return []byte("null")
		}
		return gqlExecute(ctx, "__Type", t, child, path)
	case "__schema":
		return gqlExecute(ctx, "__Schema", map[string]any{}, child, path)
	}
	return gqlMissing
}

// handleGraphQL answers the GraphQL endpoint: status and body (graphql.ts's
// handleGraphQL). A batched body (a JSON array) answers an array of bodies.
func handleGraphQL(f *forge, body jsonValue, hasToken bool) (int, []byte) {
	if !hasToken {
		return 401, []byte(`{"message":"Bad credentials","documentation_url":"https://docs.github.com/graphql"}` + "\n")
	}
	if body.kind == jsonArray {
		bodies := make([]byte, 0, 256)
		bodies = append(bodies, '[')
		for i, entry := range body.items {
			if i > 0 {
				bodies = append(bodies, ',')
			}
			bodies = append(bodies, gqlOneEntry(f, entry)...)
		}
		bodies = append(bodies, ']')
		return 200, bodies
	}
	parsed := gqlParseRequest(body)
	if parsed == nil {
		return 400, []byte(`{"message":"Problems parsing JSON"}`)
	}
	return 200, gqlOperationBody(f, parsed)
}

func gqlOneEntry(f *forge, entry jsonValue) []byte {
	parsed := gqlParseRequest(entry)
	if parsed == nil {
		return []byte(`{"errors":[{"message":"a batched operation is not a {query} object"}]}`)
	}
	return gqlOperationBody(f, parsed)
}
