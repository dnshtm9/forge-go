// MCP: the Streamable HTTP endpoint at /mcp. JSON-RPC 2.0 over POST (mcp.ts).
package main

import (
	"strconv"
	"strings"
)

const (
	mcpPreferredRevision = "2026-07-28"
)

var mcpLegacyRevisions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

const (
	jsonRPCParseError      = -32700
	jsonRPCInvalidParams   = -32602
	jsonRPCMethodNotFound  = -32601
)

const mcpServerName = "forge-core"
const mcpServerVersion = "1.0.0"

type mcpAnswer struct {
	status  int
	body    []byte
	headers [][2]string
}

type mcpToolSpec struct {
	name        string
	description string
	properties  [][2]string // name -> type
	required    []string
}

var mcpToolSpecs = []mcpToolSpec{
	{"create_repo", "Create a new repository owned by the authenticated user",
		[][2]string{{"name", "string"}, {"description", "string"}, {"private", "boolean"}}, []string{"name"}},
	{"get_repo", "Get one repository by owner and name",
		[][2]string{{"owner", "string"}, {"repo", "string"}}, []string{"owner", "repo"}},
	{"create_issue", "Create an issue in a repository",
		[][2]string{{"owner", "string"}, {"repo", "string"}, {"title", "string"}, {"body", "string"}}, []string{"owner", "repo", "title"}},
	{"list_issues", "List the issues of a repository, filtered by state",
		[][2]string{{"owner", "string"}, {"repo", "string"}, {"state", "string"}}, []string{"owner", "repo"}},
	{"create_pull_request", "Open a pull request from a head branch to a base branch",
		[][2]string{{"owner", "string"}, {"repo", "string"}, {"title", "string"}, {"head", "string"}, {"base", "string"}, {"body", "string"}}, []string{"owner", "repo", "title", "head", "base"}},
	{"merge_pull_request", "Merge (squash) one pull request",
		[][2]string{{"owner", "string"}, {"repo", "string"}, {"pull_number", "number"}}, []string{"owner", "repo", "pull_number"}},
	{"list_pull_requests", "List the pull requests of a repository",
		[][2]string{{"owner", "string"}, {"repo", "string"}, {"state", "string"}}, []string{"owner", "repo"}},
	{"get_issue", "Get one issue by number",
		[][2]string{{"owner", "string"}, {"repo", "string"}, {"issue_number", "number"}}, []string{"owner", "repo", "issue_number"}},
}

// mcpToolListBytes is the tools/list answer's tools array, built once at init.
var mcpToolListBytes = buildMCPToolList()

// buildMCPToolList answers the tool list's bytes.
func buildMCPToolList() []byte {
	j := newJSON()
	j.raw("[")
	for i, spec := range mcpToolSpecs {
		if i > 0 {
			j.raw(",")
		}
		j.begin()
		j.raw("{")
		j.key("name")
		j.string(spec.name)
		j.key("title")
		j.string(spec.name)
		j.key("description")
		j.string(spec.description)
		j.key("inputSchema")
		j.begin()
		j.raw("{")
		j.key("type")
		j.string("object")
		j.key("properties")
		j.begin()
		j.raw("{")
		for _, prop := range spec.properties {
			j.key(prop[0])
			j.raw(`{"type":`)
			j.string(prop[1])
			j.raw("}")
		}
		j.end()
		j.raw("}")
		j.key("required")
		j.raw("[")
		for k, req := range spec.required {
			if k > 0 {
				j.raw(",")
			}
			j.string(req)
		}
		j.raw("]")
		j.end()
		j.raw("}")
		j.end()
		j.raw("}")
	}
	j.raw("]")
	return j.Bytes()
}

// rpcResult frames a JSON-RPC success response.
func rpcResult(id jsonValue, result []byte) []byte {
	out := []byte(`{"jsonrpc":"2.0","id":`)
	out = append(out, jsonValueBytes(id)...)
	out = append(out, `,"result":`...)
	out = append(out, result...)
	out = append(out, '}')
	return out
}

func rpcError(id jsonValue, code int, message string) []byte {
	j := newJSON()
	j.raw(`{"jsonrpc":"2.0","id":`)
	j.buf = append(j.buf, jsonValueBytes(id)...)
	j.raw(`,"error":{"code":`)
	j.int(code)
	j.raw(`,"message":`)
	j.string(message)
	j.raw("}}")
	return j.Bytes()
}

// jsonValueBytes serializes a jsonValue compactly, keys in their parsed order.
func jsonValueBytes(v jsonValue) []byte {
	o := newJSON()
	o.jv(v)
	return o.Bytes()
}

// mcpHandle answers one /mcp request.
func handleMcp(f *forge, body jsonValue, hasToken bool, rawBody string, methodHeader string) *mcpAnswer {
	if !hasToken {
		return &mcpAnswer{status: 401, body: []byte(`{"message":"Bad credentials"}`)}
	}
	if body.kind == jsonNull {
		// A malformed body: JSON-RPC -32700 with HTTP 400.
		errBody := rpcError(jsonValue{kind: jsonNull}, jsonRPCParseError, "Invalid request body: the payload is not valid JSON")
		// mcp.ts answers {'mcp-method': methodHeader ?? ''}
		mh := methodHeader
		return &mcpAnswer{status: 400, body: errBody, headers: [][2]string{{"mcp-method", mh}}}
	}
	if body.kind != jsonObject {
		body = jsonValue{kind: jsonObject}
	}
	id, hasID := body.member("id")
	mv, _ := body.member("method")
	method := mv.strV()
	pv, hasParams := body.member("params")
	params := jsonValue{kind: jsonObject}
	if hasParams && pv.kind == jsonObject {
		params = pv
	}
	if !hasID {
		// A notification (no id): 202, no body.
		return &mcpAnswer{status: 202, body: nil}
	}

	switch method {
	case "server/discover":
		j := newJSON()
		j.raw("[")
		j.string(mcpPreferredRevision)
		for _, rev := range mcpLegacyRevisions {
			j.raw(",")
			j.string(rev)
		}
		j.raw("]")
		result := []byte(`{"resultType":"discover","supportedVersions":` + string(j.Bytes()) +
			`,"capabilities":{"tools":{"listChanged":false}}` +
			`,"serverInfo":{"name":"` + mcpServerName + `","version":"` + mcpServerVersion + `"}` +
			`,"instructions":"A GitHub-compatible forge: repositories, issues and pull requests over MCP tools."}`)
		return &mcpAnswer{status: 200, body: rpcResult(id, result)}

	case "initialize":
		negotiated := "2025-11-25"
		if v, ok := params.member("protocolVersion"); ok && v.kind == jsonString {
			negotiated = v.str
		}
		result := []byte(`{"protocolVersion":` + string(appendJSONString(nil, negotiated)) +
			`,"capabilities":{"tools":{"listChanged":false}}` +
			`,"serverInfo":{"name":"` + mcpServerName + `","version":"` + mcpServerVersion + `"}}`)
		return &mcpAnswer{status: 200, body: rpcResult(id, result)}

	case "tools/list":
		return &mcpAnswer{status: 200, body: rpcResult(id, []byte(`{"tools":`+string(buildMCPToolList())+`}`))}

	case "tools/call":
		return mcpToolsCall(f, id, params)
	}

	return &mcpAnswer{status: 200, body: rpcError(id, jsonRPCMethodNotFound, "Method not found: "+method)}
}

// mcpToolsCall answers one tools/call.
func mcpToolsCall(f *forge, id jsonValue, params jsonValue) *mcpAnswer {
	name := ""
	if v, ok := params.member("name"); ok && v.kind == jsonString {
		name = v.str
	}
	args := jsonValue{kind: jsonObject}
	if v, ok := params.member("arguments"); ok && v.kind == jsonObject {
		args = v
	}
	var spec *mcpToolSpec
	for i := range mcpToolSpecs {
		if mcpToolSpecs[i].name == name {
			spec = &mcpToolSpecs[i]
			break
		}
	}
	if spec == nil {
		return &mcpAnswer{status: 200, body: rpcError(id, jsonRPCMethodNotFound, "Tool "+name+" not found")}
	}
	var missing []string
	for _, key := range spec.required {
		v, ok := args.member(key)
		if !ok || v.kind == jsonNull || (v.kind == jsonString && v.str == "") {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		result := []byte(`{"content":[{"type":"text","text":` + string(appendJSONString(nil, "missing required argument(s): "+strings.Join(missing, ", "))) + `}],"isError":true}`)
		return &mcpAnswer{status: 200, body: rpcResult(id, result)}
	}
	content, structured, isError := mcpExecuteTool(f, name, args)
	var result []byte
	if structured != nil {
		result = []byte(`{"content":[{"type":"text","text":` + string(appendJSONString(nil, string(content))) + `}],"structuredContent":` + string(jsonValueBytes(*structured)) + `,"isError":` + strconv.FormatBool(isError) + `}`)
	} else {
		result = []byte(`{"content":[{"type":"text","text":` + string(appendJSONString(nil, string(content))) + `}],"isError":` + strconv.FormatBool(isError) + `}`)
	}
	return &mcpAnswer{status: 200, body: rpcResult(id, result)}
}

// mcpArgStr answers a tool argument's string value ("" when absent or not a string).
func mcpArgStr(args jsonValue, key string) string {
	if v, ok := args.member(key); ok && v.kind == jsonString {
		return v.str
	}
	if v, ok := args.member(strings.ReplaceAll(key, "_", "")); ok && v.kind == jsonString {
		return v.str
	}
	return ""
}

// mcpArgNumber answers a tool argument's number value.
func mcpArgNumber(args jsonValue, keys ...string) (int, bool) {
	for _, key := range keys {
		if v, ok := args.member(key); ok && v.kind == jsonNumber {
			return int(v.num), true
		}
	}
	return 0, false
}

// mcpExecuteTool runs one tool; answers its text content, optional structured
// content, and whether it reported a tool execution error.
func mcpExecuteTool(f *forge, name string, args jsonValue) (string, *jsonValue, bool) {
	owner, _ := args.member("owner")
	repoArg, _ := args.member("repo")
	fullName := ""
	if v, ok := args.member("full_name"); ok && v.kind == jsonString {
		fullName = v.str
	}
	ownerStr := jsonStr(owner)
	repoStr := jsonStr(repoArg)
	if fullName != "" && strings.Contains(fullName, "/") {
		parts := strings.SplitN(fullName, "/", 2)
		if ownerStr == "" && len(parts) == 2 {
			ownerStr = parts[0]
		}
		if repoStr == "" && len(parts) == 2 {
			repoStr = parts[1]
		}
	}
	_ = repoStr

	switch name {
	case "create_repo":
		nameArg, _ := args.member("name")
		repoName := jsonStr(nameArg)
		if repoName == "" {
			// Sibling lookup: a name property might not be spelled exactly.
			if v, ok := args.member("repo"); ok && v.kind == jsonString {
				repoName = v.str
			}
		}
		if repoName == "" {
			return "create_repo needs a name", nil, true
		}
		if _, exists := f.getRepo(f.viewerLogin, repoName); exists {
			return "name already exists on this account", nil, true
		}
		description := ""
		if v, ok := args.member("description"); ok && v.kind == jsonString {
			description = v.str
		}
		privateFlag := false
		if v, ok := args.member("private"); ok && v.kind == jsonBool {
			privateFlag = v.boolean
		}
		r := f.createRepo(f.viewerLogin, repoName, description, privateFlag)
		return mcpRepoText(r), mcpRepoStructured(r), false

	case "get_repo":
		r, ok := f.getRepo(ownerStr, repoStr)
		if !ok {
			return "repository " + ownerStr + "/" + repoStr + " not found", nil, true
		}
		return mcpRepoText(r), mcpRepoStructured(r), false

	case "create_issue":
		r, ok := f.getRepo(ownerStr, repoStr)
		if !ok {
			return "repository " + ownerStr + "/" + repoStr + " not found", nil, true
		}
		title := mcpArgStr(args, "title")
		if title == "" {
			return "create_issue needs a title", nil, true
		}
		issueRec := f.createIssue(r, title, mcpArgStr(args, "body"), []string{}, f.viewerLogin, false, nil)
		return mcpIssueText(issueRec), mcpIssueStructured(issueRec), false

	case "list_issues":
		r, ok := f.getRepo(ownerStr, repoStr)
		if !ok {
			return "repository " + ownerStr + "/" + repoStr + " not found", nil, true
		}
		state := mcpArgStr(args, "state")
		if state == "" {
			state = "open"
		}
		var issues []*issue
		for _, n := range r.issueOrder {
			if is := r.issues[n]; is != nil && !is.isPull {
				if state == "open" || state == "closed" {
					if is.state == state {
						issues = append(issues, is)
					}
				} else {
					issues = append(issues, is)
				}
			}
		}
		// sort by number desc
		for i := 1; i < len(issues); i++ {
			for j := i; j > 0 && issues[j].number > issues[j-1].number; j-- {
				issues[j], issues[j-1] = issues[j-1], issues[j]
			}
		}
		j := newJSON()
		j.raw("[")
		for i, is := range issues {
			if i > 0 {
				j.raw(",")
			}
			j.buf = append(j.buf, mcpIssueJSON(is)...)
		}
		j.raw("]")
		return string(j.Bytes()), (*jsonValue)(nil), false

	case "get_issue":
		r, ok := f.getRepo(ownerStr, repoStr)
		if !ok {
			return "repository " + ownerStr + "/" + repoStr + " not found", nil, true
		}
		number, hasNumber := mcpArgNumber(args, "issue_number", "number")
		issueRec := r.issues[number]
		if !hasNumber || issueRec == nil || issueRec.isPull {
			draw := ""
			if hasNumber {
				draw = strconv.Itoa(number)
			}
			return "issue #" + draw + " not found", nil, true
		}
		return mcpIssueText(issueRec), mcpIssueStructured(issueRec), false

	case "create_pull_request":
		r, ok := f.getRepo(ownerStr, repoStr)
		if !ok {
			return "repository " + ownerStr + "/" + repoStr + " not found", nil, true
		}
		head := mcpArgStr(args, "head")
		base := r.defaultBranch
		if v := mcpArgStr(args, "base"); v != "" {
			base = v
		}
		title := mcpArgStr(args, "title")
		if head == "" || title == "" {
			return "create_pull_request needs head and title", nil, true
		}
		headSha, hasHead := refTip(r, "refs/heads/"+head)
		baseSha, hasBase := refTip(r, "refs/heads/"+base)
		if !hasHead || !hasBase {
			return "head or base ref does not exist", nil, true
		}
		if headSha == baseSha {
			return "no commits between base and head", nil, true
		}
		p := f.createPull(r, title, mcpArgStr(args, "body"), head, base, headSha, baseSha, f.viewerLogin)
		p.files = f.diffFiles(r, baseSha, headSha)
		return mcpPullText(p), mcpPullStructured(p), false

	case "merge_pull_request":
		r, ok := f.getRepo(ownerStr, repoStr)
		if !ok {
			return "repository " + ownerStr + "/" + repoStr + " not found", nil, true
		}
		number, hasNumber := mcpArgNumber(args, "pull_number", "pr_number", "number")
		p, hasPull := r.pulls[number]
		if !hasNumber || !hasPull {
			draw := ""
			if hasNumber {
				draw = strconv.Itoa(number)
			}
			return "pull request #" + draw + " not found", nil, true
		}
		if err := mergePullInternal(f, r, p); err != "" {
			return err, nil, true
		}
		return mcpPullMergedText(p), mcpPullMergedStructured(p), false

	case "list_pull_requests":
		r, ok := f.getRepo(ownerStr, repoStr)
		if !ok {
			return "repository " + ownerStr + "/" + repoStr + " not found", nil, true
		}
		state := mcpArgStr(args, "state")
		if state == "" {
			state = "open"
		}
		var pulls []*pull
		for _, n := range r.pullOrder {
			p := r.pulls[n]
			if p == nil {
				continue
			}
			if state == "open" {
				if p.state == "open" && !p.merged {
					pulls = append(pulls, p)
				}
			} else if state == "closed" {
				if p.state == "closed" {
					pulls = append(pulls, p)
				}
			} else {
				pulls = append(pulls, p)
			}
		}
		for i := 1; i < len(pulls); i++ {
			for j := i; j > 0 && pulls[j].number > pulls[j-1].number; j-- {
				pulls[j], pulls[j-1] = pulls[j-1], pulls[j]
			}
		}
		j := newJSON()
		j.raw("[")
		for i, p := range pulls {
			if i > 0 {
				j.raw(",")
			}
			j.buf = append(j.buf, mcpPullJSON(p)...)
		}
		j.raw("]")
		return string(j.Bytes()), nil, false
	}
	return "tool " + name + " not found", nil, true
}

func jsonStr(v jsonValue) string { return v.strV() }

// mcpRepoJSON answers the tool's repo shape.
func mcpRepoJSON(r *repo) []byte {
	j := newJSON()
	j.raw("{")
	j.begin()
	j.key("id")
	j.int(r.id)
	j.key("name")
	j.string(r.name)
	j.key("full_name")
	j.string(r.owner + "/" + r.name)
	j.key("private")
	j.boolean(r.privateFlag)
	j.key("description")
	j.string(r.description)
	j.key("default_branch")
	j.string(r.defaultBranch)
	j.key("owner")
	j.raw(`{"login":`)
	j.string(r.owner)
	j.end()
	j.raw("}")
	j.end()
	j.raw("}")
	return j.Bytes()
}

func mcpRepoText(r *repo) string { return "repository " + string(mcpRepoJSON(r)) }

func mcpRepoStructured(r *repo) *jsonValue {
	v, ok := parseJSON(string(mcpRepoJSON(r)))
	if !ok {
		return nil
	}
	return &v
}

func mcpIssueJSON(is *issue) []byte {
	j := newJSON()
	j.raw("{")
	j.begin()
	j.key("id")
	j.int(is.id)
	j.key("number")
	j.int(is.number)
	j.key("title")
	j.string(is.title)
	j.key("body")
	j.string(is.body)
	j.key("state")
	j.string(is.state)
	j.end()
	j.raw("}")
	return j.Bytes()
}

func mcpIssueText(is *issue) string { return "issue " + string(mcpIssueJSON(is)) }

func mcpIssueStructured(is *issue) *jsonValue {
	v, ok := parseJSON(string(mcpIssueJSON(is)))
	if !ok {
		return nil
	}
	return &v
}

func mcpPullJSON(p *pull) []byte {
	j := newJSON()
	j.raw("{")
	j.begin()
	j.key("id")
	j.int(p.id)
	j.key("number")
	j.int(p.number)
	j.key("title")
	j.string(p.title)
	j.key("state")
	if p.merged {
		j.string("closed")
	} else {
		j.string(p.state)
	}
	j.key("merged")
	j.boolean(p.merged)
	j.key("head")
	j.raw(`{"ref":`)
	j.string(p.head)
	j.raw("}")
	j.key("base")
	j.raw(`{"ref":`)
	j.string(p.base)
	j.raw("}")
	j.end()
	j.raw("}")
	return j.Bytes()
}

func mcpPullText(p *pull) string { return "pull request " + string(mcpPullJSON(p)) }

func mcpPullStructured(p *pull) *jsonValue {
	v, ok := parseJSON(string(mcpPullJSON(p)))
	if !ok {
		return nil
	}
	return &v
}

// mcpPullMergedText answers the merged pull's shape (with merged:true and sha).
func mcpPullMergedJSON(p *pull) []byte {
	j := newJSON()
	j.raw("{")
	j.begin()
	j.key("id")
	j.int(p.id)
	j.key("number")
	j.int(p.number)
	j.key("title")
	j.string(p.title)
	j.key("state")
	if p.merged {
		j.string("closed")
	} else {
		j.string(p.state)
	}
	j.key("merged")
	j.boolean(true)
	j.key("head")
	j.raw(`{"ref":`)
	j.string(p.head)
	j.raw("}")
	j.key("base")
	j.raw(`{"ref":`)
	j.string(p.base)
	j.raw("}")
	j.key("sha")
	j.strPtr(p.mergeCommitSha)
	j.end()
	j.raw("}")
	return j.Bytes()
}

func mcpPullMergedText(p *pull) string { return "pull request " + string(mcpPullMergedJSON(p)) }

func mcpPullMergedStructured(p *pull) *jsonValue {
	v, ok := parseJSON(string(mcpPullMergedJSON(p)))
	if !ok {
		return nil
	}
	return &v
}

// mcpParseBody parses a raw MCP body: the parsed jsonValue, answering null for
// an empty or malformed body (mcp.ts's parseJsonOr(null)).
func mcpParseBody(rawBody string) jsonValue {
	if strings.TrimSpace(rawBody) == "" {
		return jsonValue{kind: jsonNull}
	}
	v, ok := parseJSON(rawBody)
	if !ok {
		return jsonValue{kind: jsonNull}
	}
	return v
}
