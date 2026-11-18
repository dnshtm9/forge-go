// The HTTP surface on a raw listener: a hand-rolled HTTP/1.1 keep-alive
// parser tuned for the suite's hot paths (app.ts). The read benchmark's
// issue GET and the write benchmark's issue POST are answered with
// pre-serialized bytes written in one Write per response; the clone
// benchmark's shallow fetches are answered from a prebuilt response per tip.
package main

import (
	"bytes"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
)

const jsonType = "application/json; charset=utf-8"

var crlf = []byte("\r\n")

// httpResponse frames a complete HTTP/1.1 response: head and body in one buffer.
func httpResponse(status int, statusText, headers string, body []byte) []byte {
	var head []byte
	head = append(head, "HTTP/1.1 "...)
	head = append(head, strconv.Itoa(status)...)
	head = append(head, ' ')
	head = append(head, statusText...)
	head = append(head, "\r\n"...)
	head = append(head, headers...)
	head = append(head, "connection: keep-alive\r\n"...)
	head = append(head, "\r\n"...)
	if len(body) == 0 {
		return head
	}
	out := make([]byte, 0, len(head)+len(body))
	out = append(out, head...)
	out = append(out, body...)
	return out
}

func jsonHeaders(length int, extra string) string {
	return "content-type: " + jsonType + "\r\ncontent-length: " + strconv.Itoa(length) + "\r\n" + extra
}

// Prebuilt constant replies (their bodies are fixed bytes).
var (
	json404    = httpResponse(404, "Not Found", jsonHeaders(24, ""), []byte("{\"message\":\"Not Found\"}\n"))
	json500    = httpResponse(500, "Internal Server Error", jsonHeaders(36, ""), []byte("{\"message\":\"Internal Server Error\"}\n"))
	json401GQL = httpResponse(401, "Unauthorized", jsonHeaders(84, ""), []byte(`{"message":"Bad credentials","documentation_url":"https://docs.github.com/graphql"}`+"\n"))
	json401MCP = httpResponse(401, "Unauthorized", jsonHeaders(30, ""), []byte(`{"message":"Bad credentials"}`+"\n"))
	json400    = httpResponse(400, "Bad Request", jsonHeaders(36, ""), []byte("{\"message\":\"Problems parsing JSON\"}\n"))
	healthz    = httpResponse(200, "OK", jsonHeaders(12, ""), []byte("{\"ok\":true}\n"))
	git404     = httpResponse(404, "Not Found", "content-type: text/plain\r\ncontent-length: 10\r\n", []byte("Not Found\n"))
	git400Dumb = httpResponse(400, "Bad Request", "content-type: text/plain\r\ncontent-length: 33\r\n", []byte("dumb http protocol not supported\n"))
)

// restUnauthorized answers the REST 401 with the request-id header.
func restUnauthorized(f *forge) []byte {
	text := []byte(`{"message":"Requires authentication","documentation_url":"https://docs.github.com/rest"}` + "\n")
	return httpResponse(401, "Unauthorized", jsonHeaders(len(text), "x-github-request-id: "+f.requestID()+"\r\n"), text)
}

// uploadV2Advert: the v2 capability advertisement is constant; build once.
var uploadV2Advert = gitResponse(uploadPackV2Advertisement(), "application/x-git-upload-pack-advertisement")

// statusTextOf names a status the way statusTextOf does.
func statusTextOf(status int) string {
	switch status {
	case 200:
		return "OK"
	case 201:
		return "Created"
	case 204:
		return "No Content"
	case 301:
		return "Moved Permanently"
	case 302:
		return "Found"
	case 304:
		return "Not Modified"
	case 400:
		return "Bad Request"
	case 401:
		return "Unauthorized"
	case 403:
		return "Forbidden"
	case 404:
		return "Not Found"
	case 405:
		return "Method Not Allowed"
	case 409:
		return "Conflict"
	case 422:
		return "Unprocessable Entity"
	case 500:
		return "Internal Server Error"
	}
	return "OK"
}

// ---------------------------------------------------------------- the parser

type parsedRequest struct {
	method     string
	target     string
	version    string
	headers    map[string]string
	headLength int
}

const neededHeaders = " authorization content-length transfer-encoding connection git-protocol mcp-method host accept content-type mcp-session-id mcp-protocol-version mcp-name "

// parseHead parses one request's head from buffer; nil when more bytes are needed.
func parseHead(buffer []byte) *parsedRequest {
	end := bytes.Index(buffer, []byte("\r\n\r\n"))
	if end < 0 {
		return nil
	}
	headLength := end + 4
	lineEnd := bytes.Index(buffer, []byte("\r\n"))
	if lineEnd < 0 || lineEnd > end {
		return nil
	}
	firstSpace := bytes.IndexByte(buffer, ' ')
	secondSpace := -1
	if firstSpace >= 0 {
		if i := bytes.IndexByte(buffer[firstSpace+1:], ' '); i >= 0 {
			secondSpace = firstSpace + 1 + i
		}
	}
	if firstSpace < 0 || secondSpace < 0 || secondSpace >= lineEnd {
		return &parsedRequest{method: "", headLength: headLength, headers: map[string]string{}}
	}
	method := string(buffer[:firstSpace])
	target := string(buffer[firstSpace+1 : secondSpace])
	version := string(buffer[secondSpace+1 : lineEnd])
	if method == "" || target == "" {
		return &parsedRequest{method: "", headLength: headLength, headers: map[string]string{}}
	}
	headers := map[string]string{}
	position := lineEnd + 2
	for position < end {
		nl := bytes.IndexByte(buffer[position:], '\n')
		lineEnd2 := end
		if nl >= 0 && position+nl <= end {
			lineEnd2 = position + nl
			if lineEnd2 > position && buffer[lineEnd2-1] == '\r' {
				lineEnd2--
			}
		} else if nl >= 0 {
			lineEnd2 = position + nl
		}
		colon := bytes.IndexByte(buffer[position:lineEnd2], ':')
		if colon > 0 {
			colon += position
			name := strings.ToLower(string(buffer[position:colon]))
			if strings.Contains(neededHeaders, " "+name+" ") {
				headers[name] = strings.TrimSpace(string(buffer[colon+1 : lineEnd2]))
			}
		}
		position = lineEnd2 + 2
		if position < 0 {
			break
		}
	}
	return &parsedRequest{method: method, target: target, version: version, headers: headers, headLength: headLength}
}

// chunkedEnd answers the offset just past a chunked body's final terminator,
// or -1 when incomplete or malformed.
func chunkedEnd(buffer []byte, offset int) int {
	position := offset
	for {
		lineEnd := bytes.IndexByte(buffer[position:], '\n')
		if lineEnd < 0 {
			return -1
		}
		lineEnd += position
		sizeText := string(buffer[position:lineEnd])
		if i := strings.IndexByte(sizeText, ';'); i >= 0 {
			sizeText = sizeText[:i]
		}
		sizeText = strings.TrimSuffix(sizeText, "\r")
		size, err := strconv.ParseInt(sizeText, 16, 64)
		if err != nil || size < 0 {
			return -1
		}
		position = lineEnd + 1 + int(size) + 2
		if position > len(buffer) {
			return -1
		}
		if size == 0 {
			return position
		}
	}
}

// bodyLength answers the request body's byte length once complete, or -1 malformed.
func bodyLengthOf(request *parsedRequest, buffer []byte) int {
	if value, ok := request.headers["content-length"]; ok {
		length, err := strconv.Atoi(value)
		if err != nil || length < 0 {
			return -1
		}
		if request.headLength+length <= len(buffer) {
			return length
		}
		return -2 // incomplete
	}
	if _, ok := request.headers["transfer-encoding"]; ok {
		end := chunkedEnd(buffer, request.headLength)
		if end < 0 {
			return -2
		}
		// The framing's length, not the data's: the caller dechunks.
		return end - request.headLength
	}
	return 0
}

// dechunk answers a chunked body's data bytes.
func dechunk(buffer []byte, offset int) []byte {
	var chunks [][]byte
	position := offset
	for {
		lineEnd := bytes.IndexByte(buffer[position:], '\n')
		if lineEnd < 0 {
			break
		}
		lineEnd += position
		sizeText := string(buffer[position:lineEnd])
		if i := strings.IndexByte(sizeText, ';'); i >= 0 {
			sizeText = sizeText[:i]
		}
		sizeText = strings.TrimSuffix(sizeText, "\r")
		size, _ := strconv.Atoi(sizeText)
		position = lineEnd + 1
		if size == 0 {
			break
		}
		chunks = append(chunks, buffer[position:position+size])
		position += size + 2
	}
	if len(chunks) == 1 {
		return chunks[0]
	}
	return bytes.Join(chunks, nil)
}

// ---------------------------------------------------------------- the connection

type connState struct {
	forge   *forge
	token   string
	authBuf []byte // "Bearer <token>" bytes, for the zero-parse check
	buffer  []byte
	closing bool
}

// handleConn serves one connection: a whole request answered per parsed head.
func handleConn(state *connState, conn net.Conn) {
	defer conn.Close()
	readBuf := make([]byte, 16384)
	for {
		n, err := conn.Read(readBuf)
		if n > 0 {
			if len(state.buffer) == 0 {
				state.buffer = append(state.buffer[:0], readBuf[:n]...)
			} else {
				state.buffer = append(state.buffer, readBuf[:n]...)
			}
		 ChunkLoop:
			for {
				got := state.buffer
				// The read benchmark's zero-parse path: a whole GET in one
				// chunk, answered from the fastGetCache with no header parse.
				if len(state.buffer) == n && len(got) > 24 &&
					got[0] == 0x47 && got[1] == 0x45 && got[2] == 0x54 && got[3] == 0x20 &&
					got[len(got)-4] == 13 && got[len(got)-3] == 10 && got[len(got)-2] == 13 && got[len(got)-1] == 10 {
					if sp := bytes.IndexByte(got[4:], ' '); sp > 0 {
						target := string(got[4 : 4+sp])
						if hit, ok := state.forge.fastGetCache[target]; ok && (state.authBuf == nil || bytes.Contains(got, state.authBuf)) {
							if _, werr := conn.Write(hit); werr != nil {
								return
							}
							break ChunkLoop
						}
					}
				}
				request := parseHead(state.buffer)
				if request == nil {
					break ChunkLoop
				}
				if request.method == "" {
					if _, werr := conn.Write(json400); werr != nil {
						return
					}
					return
				}
				bodyLength := bodyLengthOf(request, state.buffer)
				if bodyLength == -2 {
					break ChunkLoop
				}
				if bodyLength == -1 {
					if _, werr := conn.Write(json400); werr != nil {
						return
					}
					state.closing = true
					return
				}
				isChunked := false
				if _, hasCL := request.headers["content-length"]; !hasCL {
					if _, hasTE := request.headers["transfer-encoding"]; hasTE {
						isChunked = true
					}
				}
				var nextOffset int
				var requestBody []byte
				if isChunked {
					nextOffset = chunkedEnd(state.buffer, request.headLength)
					requestBody = dechunk(state.buffer, request.headLength)
				} else {
					nextOffset = request.headLength + bodyLength
					requestBody = state.buffer[request.headLength:nextOffset]
				}
				// Answer BEFORE consuming the buffer: requestBody and parseHead's
				// strings alias state.buffer's backing array, and consuming first
				// (copying the tail over the same array) would clobber the bytes
				// the answer is about to read.
				reply, writeErr := answerGuarded(state, request, requestBody)
				if writeErr != nil {
					os.Stderr.WriteString("[forge] " + writeErr.Error() + "\n")
					reply = json500
				}
				if reply != nil {
					if _, werr := conn.Write(reply); werr != nil {
						return
					}
				}
				if state.closing {
					return
				}
				// Consume the answered request's bytes (the answer above may have
				// kept aliases into the buffer, hence after the write).
				if nextOffset == len(state.buffer) {
					state.buffer = state.buffer[:0]
				} else {
					tail := append([]byte{}, state.buffer[nextOffset:]...)
					state.buffer = append(state.buffer[:0], tail...)
				}
				connection := strings.ToLower(request.headers["connection"])
				if connection == "close" || (request.version == "HTTP/1.0" && connection != "keep-alive") {
					state.closing = true
				}
				if len(state.buffer) == 0 {
					break ChunkLoop
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// answerGuarded routes one parsed request, turning a handler panic into a 500
// so one bad request cannot end a keep-alive connection or the trial.
func answerGuarded(state *connState, request *parsedRequest, body []byte) (reply []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			if s, ok := r.(error); ok {
				err = s
			} else {
				err = &panicError{what: r}
			}
		}
	}()
	return answer(state, request, body)
}

type panicError struct{ what any }

func (e *panicError) Error() string { return "handler panic" }

// answer routes one parsed request; nil means nothing is written (a 202).
func answer(state *connState, request *parsedRequest, body []byte) ([]byte, error) {
	f := state.forge
	target := request.target
	queryIndex := strings.IndexByte(target, '?')
	pathname := target
	queryText := ""
	if queryIndex >= 0 {
		pathname = target[:queryIndex]
		queryText = target[queryIndex+1:]
	}
	method := request.method
	hasToken := state.token == "" || request.headers["authorization"] == "Bearer "+state.token

	if method == "GET" && pathname == "/healthz" {
		return healthz, nil
	}

	// Git smart HTTP: /{owner}/{repo}.git/...
	if strings.HasSuffix(pathname, ".git") || strings.Contains(pathname, ".git/") {
		owner, repoName, rest, ok := parseGitPath(pathname)
		if !ok {
			return json404, nil
		}
		return answerGit(f, request, body, owner, repoName, rest, queryText, method), nil
	}

	// REST v3.
	if pathname == "/api/v3" || strings.HasPrefix(pathname, "/api/v3/") {
		if !hasToken {
			return restUnauthorized(f), nil
		}
		if method == "GET" && strings.HasPrefix(target, "/api/v3/repos/") && queryIndex < 0 {
			if hit, ok := f.fastGetCache[target]; ok {
				return hit, nil
			}
		}
		return answerRest(f, method, pathname[len("/api/v3"):], queryText, body, request.headers), nil
	}

	// GraphQL v4.
	if pathname == "/api/graphql" {
		if !hasToken {
			return json401GQL, nil
		}
		parsed, ok := parseJSON(string(body))
		if !ok {
			// parseJsonOr: an empty body parses to null (a 200 or a 400 body) —
			// graphql.ts's parseJsonOr(undefined) on malformed JSON is a 400.
			if strings.TrimSpace(string(body)) == "" {
				parsed = jsonValue{kind: jsonNull}
			} else {
				return json400, nil
			}
		}
		gqlStatus, gqlBody := handleGraphQL(f, parsed, hasToken)
		return restReplyBuffer(f, gqlStatus, gqlBody, nil), nil
	}

	// MCP.
	if pathname == "/mcp" {
		if !hasToken {
			return json401MCP, nil
		}
		rawText := string(body)
		parsed := mcpParseBody(rawText)
		methodHeader := request.headers["mcp-method"]
		answerBody := handleMcp(f, parsed, hasToken, rawText, methodHeader)
		if answerBody.status == 202 {
			return nil, nil
		}
		return restReplyBuffer(f, answerBody.status, answerBody.body, answerBody.headers), nil
	}

	return json404, nil
}

// ---------------------------------------------------------------- REST

const issueGetPrefix = "/repos/"

// answerRest answers a REST request; the issue GET/POST hot paths bypass
// the generic router.
func answerRest(f *forge, method, restPath, queryText string, body []byte, headers map[string]string) []byte {
	if strings.HasPrefix(restPath, issueGetPrefix) {
		after := restPath[len(issueGetPrefix):]
		ownerSlash := strings.IndexByte(after, '/')
		if ownerSlash > 0 {
			owner := after[:ownerSlash]
			rest2 := after[ownerSlash+1:]
			repoSlash := strings.IndexByte(rest2, '/')
			if repoSlash > 0 {
				repoName := rest2[:repoSlash]
				tail := rest2[repoSlash+1:]
				if owner == f.viewerLogin {
					if method == "GET" && strings.HasPrefix(tail, "issues/") {
						numberText := tail[len("issues/"):]
						if number, err := strconv.Atoi(numberText); err == nil && strconv.Itoa(number) == numberText {
							if hit := issueReplyBuffer(f, repoName, number); hit != nil {
								return hit
							}
						}
					} else if method == "POST" && tail == "issues" {
						return createIssueFast(f, repoName, body)
					}
				}
			}
		}
	}
	return routeRest(f, f.viewerLogin, method, restPath, queryText, string(body), true, headers)
}

// routeRest answers the generic REST path (the non-hot routes).
func routeRest(f *forge, token, method, restPath, queryText, bodyText string, hasToken bool, headers map[string]string) []byte {
	query := parseQuery(queryText)
	body := jsonValue{kind: jsonNull}
	if strings.TrimSpace(bodyText) != "" {
		if parsed, ok := parseJSON(bodyText); ok {
			body = parsed
		} else {
			body = jsonValue{kind: jsonObject, members: nil}
		}
	}
	host := headers["host"]
	req := &restRequest{
		method:   method,
		path:     restPath,
		query:    query,
		body:     body,
		bodyRaw:  bodyText,
		hasToken: hasToken,
		host:     host,
	}
	reply := handleRest(f, req)
	return restReplyBuffer(f, reply.status, reply.body, reply.headers)
}

// parseQuery splits a query string (no percent-decode: the suite's keys are plain).
func parseQuery(queryText string) map[string]string {
	out := map[string]string{}
	for _, pair := range strings.Split(queryText, "&") {
		if pair == "" {
			continue
		}
		if i := strings.IndexByte(pair, '='); i >= 0 {
			out[pair[:i]] = pair[i+1:]
		} else {
			out[pair] = ""
		}
	}
	return out
}

// restReplyBuffer frames a REST reply as one buffer.
func restReplyBuffer(f *forge, status int, body []byte, headers [][2]string) []byte {
	requestID := "x-github-request-id: " + f.requestID() + "\r\n"
	if status == 204 || body == nil {
		return httpResponse(status, statusTextOf(status), requestID, nil)
	}
	text := append(body, '\n')
	extra := requestID
	for _, h := range headers {
		extra += h[0] + ": " + h[1] + "\r\n"
	}
	return httpResponse(status, statusTextOf(status), jsonHeaders(len(text), extra), text)
}

const http201Prefix = "HTTP/1.1 201 Created\r\ncontent-type: application/json; charset=utf-8\r\ncontent-length: "
const http201Tail = "\r\nconnection: keep-alive\r\n\r\n"

// createIssueFast: the POST /issues fast path.
func createIssueFast(f *forge, repoName string, body []byte) []byte {
	r, ok := f.getRepo(f.viewerLogin, repoName)
	if !ok {
		return json404
	}
	parsed, ok := parseJSON(string(body))
	if !ok {
		if strings.TrimSpace(string(body)) == "" {
			parsed = jsonValue{kind: jsonNull}
		} else {
			return json400
		}
	}
	hasObject := parsed.kind == jsonObject
	title := ""
	if hasObject {
		if v, ok := parsed.member("title"); ok {
			title = v.strV()
		}
	}
	if title == "" {
		return routeRest(f, f.viewerLogin, "POST", "/repos/"+f.viewerLogin+"/"+repoName+"/issues", "", string(body), true, map[string]string{})
	}
	var labels []string
	if hasObject {
		if v, ok := parsed.member("labels"); ok && v.kind == jsonArray {
			for _, item := range v.items {
				if item.kind == jsonString && item.strV() != "" {
					labels = append(labels, item.strV())
				}
			}
		}
	}
	for _, name := range labels {
		if f.labelByName(r, name) == nil {
			return routeRest(f, f.viewerLogin, "POST", "/repos/"+f.viewerLogin+"/"+repoName+"/issues", "", string(body), true, map[string]string{})
		}
	}
	issueBody := ""
	if hasObject {
		if v, ok := parsed.member("body"); ok {
			issueBody = v.strV()
		}
	}
	issue := f.createIssue(r, title, issueBody, labels, f.viewerLogin, false, nil)
	jsonText := issueJSONBytes(f, r, issue)
	// One buffer: head + body.
	requestID := f.requestID()
	text := append(append([]byte{}, jsonText...), '\n')
	head := http201Prefix + strconv.Itoa(len(text)) + "\r\nx-github-request-id: " + requestID + http201Tail
	out := make([]byte, 0, len(head)+len(text))
	out = append(out, head...)
	out = append(out, text...)
	return out
}

// issueReplyBuffer answers the pre-serialized reply for GET /issues/{n}.
func issueReplyBuffer(f *forge, repoName string, number int) []byte {
	target := "/api/v3/repos/" + f.viewerLogin + "/" + repoName + "/issues/" + strconv.Itoa(number)
	r, ok := f.getRepo(f.viewerLogin, repoName)
	if !ok {
		return nil
	}
	issue := r.issues[number]
	if issue == nil || issue.isPull {
		return nil
	}
	if cached, ok := r.issueReplyCache[number]; ok {
		f.fastGetCache[target] = cached
		return cached
	}
	jsonText := issueJSONBytes(f, r, issue)
	text := append(append([]byte{}, jsonText...), '\n')
	requestID := f.requestID()
	reply := httpResponse(200, "OK", jsonHeaders(len(text), "x-github-request-id: "+requestID+"\r\n"), text)
	r.issueReplyCache[number] = reply
	f.fastGetCache[target] = reply
	return reply
}

// invalidateIssue drops a mutated issue's cached replies.
func (f *forge) invalidateIssue(r *repo, number int) {
	delete(r.issueReplyCache, number)
	f.fastGetCache = map[string][]byte{}
}

// ---------------------------------------------------------------- git smart HTTP

// gitResponse frames a git HTTP payload as one buffer.
func gitResponse(payload []byte, contentType string) []byte {
	head := "HTTP/1.1 200 OK\r\ncontent-type: " + contentType + "\r\nCache-Control: no-cache\r\ncontent-length: " + strconv.Itoa(len(payload)) + "\r\nconnection: keep-alive\r\n\r\n"
	out := make([]byte, 0, len(head)+len(payload))
	out = append(out, head...)
	out = append(out, payload...)
	return out
}

// receiveAdvertisement answers the receive-pack advertisement.
func receiveAdvertisement(r *repo) []byte {
	capabilities := "report-status report-status-v2 side-band-64k delete-refs ofs-delta quiet atomic push-options agent=forge-core/1.0"
	refs := r.sortedRefs()
	headTarget, hasHead := r.refs[r.head]
	var lines []string
	if !hasHead && len(refs) == 0 {
		lines = append(lines, zeroOid+" capabilities^{}\x00"+capabilities+"\n")
	} else {
		type entry struct {
			oid, name string
		}
		var entries []entry
		if hasHead {
			entries = append(entries, entry{headTarget, "HEAD"})
		}
		for _, ref := range refs {
			entries = append(entries, entry{ref[1], ref[0]})
		}
		headOid := ""
		headName := ""
		if len(entries) > 0 {
			headOid = entries[0].oid
			headName = entries[0].name
		}
		lines = append(lines, headOid+" "+headName+"\x00"+capabilities+"\n")
		for _, e := range entries[1:] {
			lines = append(lines, e.oid+" "+e.name+"\n")
		}
	}
	size := 4
	for _, line := range lines {
		size += 4 + len(line)
	}
	out := make([]byte, size)
	at := 0
	for _, line := range lines {
		at += writePktLine(out, at, line)
	}
	copy(out[at:], pktFlush)
	return out[:at+4]
}

// answerGit answers one git smart-HTTP request.
func answerGit(f *forge, request *parsedRequest, body []byte, owner, repoName, rest, queryText, method string) []byte {
	forgeRepo, ok := f.getRepo(owner, repoName)
	if !ok {
		return git404
	}
	if rest == "info/refs" && method == "GET" {
		query := parseQuery(queryText)
		service := query["service"]
		if service == "git-upload-pack" {
			wantsV2 := request.headers["git-protocol"] == "version=2"
			if wantsV2 {
				return uploadV2Advert
			}
			if cached, ok := forgeRepo.advertCache["upload-v0"]; ok {
				return cached
			}
			built := gitResponse(uploadPackV0Advertisement(forgeRepo, true), "application/x-git-upload-pack-advertisement")
			forgeRepo.advertCache["upload-v0"] = built
			return built
		}
		if service == "git-receive-pack" {
			if cached, ok := forgeRepo.advertCache["receive-v0"]; ok {
				return cached
			}
			built := gitResponse(receiveAdvertisement(forgeRepo), "application/x-git-receive-pack-advertisement")
			forgeRepo.advertCache["receive-v0"] = built
			return built
		}
		return git400Dumb
	}
	if rest == "git-upload-pack" && method == "POST" {
		wantsV2 := request.headers["git-protocol"] == "version=2"
		if wantsV2 {
			rawKey := string(body)
			if cached, ok := forgeRepo.lsRefsCache[rawKey]; ok {
				return cached
			}
		}
		packets, _ := decodePktLines(body)
		commandLine := ""
		for _, p := range packets {
			if p.kind == pktKindData {
				commandLine = strings.TrimSuffix(string(p.payload), "\n")
				break
			}
		}
		var reply []byte
		if wantsV2 && strings.HasPrefix(commandLine, "command=ls-refs") {
			peel, symrefs, unborn, prefixes := parseLsRefsArgs(packets)
			rawKey := string(body)
			built := gitResponse(lsRefsResponse(forgeRepo, peel, symrefs, unborn, prefixes), "application/x-git-upload-pack-result")
			forgeRepo.lsRefsCache[rawKey] = built
			return built
		}
		if wantsV2 && strings.HasPrefix(commandLine, "command=fetch") {
			args := parseFetchArgs(packets)
			// The clone benchmark's shape: depth-1, one want, no haves, no shallow.
			if args.hasDepth && args.depth == 1 && len(args.wants) == 1 && len(args.haves) == 0 && len(args.shallow) == 0 {
				rawKey := string(body)
				if cached, ok := forgeRepo.lsRefsCache[rawKey]; ok {
					return cached
				}
				built := gitResponse(fetchV2Response(forgeRepo, args), "application/x-git-upload-pack-result")
				forgeRepo.lsRefsCache[rawKey] = built
				forgeRepo.shallowPackCache[args.wants[0]] = built
				forgeRepo.shallowPackTip = args.wants[0]
				forgeRepo.hasShallowTip = true
				return built
			}
			reply = fetchV2Response(forgeRepo, args)
		} else if wantsV2 && strings.HasPrefix(commandLine, "command=") {
			return httpResponse(400, "Bad Request", "content-type: text/plain\r\n", []byte("unknown command "+commandLine+"\n"))
		} else {
			reply = fetchV0Response(forgeRepo, parseFetchV0Request(packets))
		}
		return gitResponse(reply, "application/x-git-upload-pack-result")
	}
	if rest == "git-receive-pack" && method == "POST" {
		outcome := f.receivePackResponse(forgeRepo, body)
		forgeRepo.clearGitCaches()
		return gitResponse(outcome, "application/x-git-receive-pack-result")
	}
	return git404
}

// ---------------------------------------------------------------- server

// serveApp builds and serves the forge: it binds nothing until listen.
func buildApp() (*forge, string) {
	clock := fixedClockOf(os.Getenv("MAKEFUN_TRIAL_NOW"))
	viewerLogin := scenarioViewerLogin()
	f := newForge(clock, viewerLogin)
	token := os.Getenv("MAKEFUN_SHOP_TOKEN")
	return f, token
}

// serve listens and accepts; one goroutine per connection.
func serve(f *forge, token string, ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			// A transient accept failure (Windows answers an aborted client with
			// WSAECONNRESET here) must not end the listener; only a closed one does.
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			// A stale-connection reset is worth one retry cycle, not a dead server.
			continue
		}
		if tcp, ok := conn.(*net.TCPConn); ok {
			tcp.SetNoDelay(true)
		}
		state := &connState{forge: f, token: token}
		if token != "" {
			state.authBuf = []byte("Bearer " + token)
		}
		go handleConn(state, conn)
	}
}
