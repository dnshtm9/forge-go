// The fetch and receive-pack halves of git smart HTTP, appended to git-smart.go.
package main

import (
	"bytes"
	"sort"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------- fetch

type fetchArgs struct {
	wants    []string
	haves    []string
	depth    int
	hasDepth bool
	shallow  []string
	done     bool
}

// walkForFetch answers the commits a depth-limited walk keeps and the shallow boundary.
func walkForFetch(r *repo, wants []string, depth int, hasDepth bool) (map[string]bool, []string) {
	keep := map[string]bool{}
	shallowSet := map[string]bool{}
	type item struct {
		oid   string
		depth int
	}
	queue := make([]item, 0, len(wants))
	for _, want := range wants {
		queue = append(queue, item{oid: want, depth: 1})
	}
	for len(queue) > 0 {
		it := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if keep[it.oid] {
			continue
		}
		object, ok := r.objects[it.oid]
		if !ok {
			continue
		}
		keep[it.oid] = true
		if object.typ == typeTag {
			target := peelTo(r.objects, it.oid)
			if !keep[target] {
				queue = append(queue, item{oid: target, depth: it.depth})
			}
			continue
		}
		if object.typ != typeCommit {
			continue
		}
		c, err := parseCommit(object.data)
		if err != nil {
			continue
		}
		atLimit := hasDepth && it.depth >= depth
		if atLimit {
			if len(c.parents) > 0 {
				shallowSet[it.oid] = true
			}
			continue
		}
		for _, parent := range c.parents {
			queue = append(queue, item{oid: parent, depth: it.depth + 1})
		}
	}
	shallow := make([]string, 0, len(shallowSet))
	for oid := range shallowSet {
		shallow = append(shallow, oid)
	}
	sort.Strings(shallow)
	return keep, shallow
}

// objectsForFetch answers the closure of objects to send: everything reachable
// from keep, cut at the shallow boundaries, minus the client's haves' closure.
func objectsForFetch(r *repo, keep map[string]bool, haves []string, boundaries map[string]bool, clientShallow []string) []gitObject {
	clientBoundaries := map[string]bool{}
	for _, oid := range clientShallow {
		clientBoundaries[oid] = true
	}
	haveClosure := map[string]bool{}
	haveQueue := append([]string{}, haves...)
	for len(haveQueue) > 0 {
		oid := haveQueue[len(haveQueue)-1]
		haveQueue = haveQueue[:len(haveQueue)-1]
		if haveClosure[oid] {
			continue
		}
		object, ok := r.objects[oid]
		if !ok {
			continue
		}
		haveClosure[oid] = true
		switch object.typ {
		case typeCommit:
			c, err := parseCommit(object.data)
			if err != nil {
				continue
			}
			haveQueue = append(haveQueue, c.tree)
			if !clientBoundaries[oid] {
				haveQueue = append(haveQueue, c.parents...)
			}
		case typeTag:
			haveQueue = append(haveQueue, peelTo(r.objects, oid))
		case typeTree:
			entries, err := parseTree(object.data)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if e.mode != "160000" {
					haveQueue = append(haveQueue, e.oid)
				}
			}
		}
	}
	var out []gitObject
	seen := map[string]bool{}
	var queue []string
	for oid := range keep {
		if object, ok := r.objects[oid]; ok && (object.typ == typeCommit || object.typ == typeTag) {
			queue = append(queue, oid)
		}
	}
	for len(queue) > 0 {
		oid := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if seen[oid] || haveClosure[oid] {
			continue
		}
		object, ok := r.objects[oid]
		if !ok {
			continue
		}
		seen[oid] = true
		out = append(out, object)
		switch object.typ {
		case typeCommit:
			c, err := parseCommit(object.data)
			if err != nil {
				continue
			}
			queue = append(queue, c.tree)
			if !boundaries[oid] {
				queue = append(queue, c.parents...)
			}
		case typeTag:
			queue = append(queue, peelTo(r.objects, oid))
		case typeTree:
			entries, err := parseTree(object.data)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if e.mode != "160000" {
					queue = append(queue, e.oid)
				}
			}
		}
	}
	return out
}

func buildPack(objects []gitObject) []byte {
	entries := make([]packEntryIn, len(objects))
	for i, o := range objects {
		entries[i] = packEntryIn{typ: o.typ, data: o.data}
	}
	return writePack(entries)
}

func fetchV2Response(r *repo, args fetchArgs) []byte {
	keep, shallow := walkForFetch(r, args.wants, args.depth, args.hasDepth)
	boundaries := map[string]bool{}
	for _, s := range shallow {
		boundaries[s] = true
	}
	objects := objectsForFetch(r, keep, args.haves, boundaries, args.shallow)

	var parts [][]byte
	clientShallow := map[string]bool{}
	for _, s := range args.shallow {
		clientShallow[s] = true
	}
	shallowSet := map[string]bool{}
	for _, s := range shallow {
		shallowSet[s] = true
	}
	var shallowLines, unshallowLines []string
	for _, oid := range shallow {
		found := false
		for _, s := range args.shallow {
			if s == oid {
				found = true
				break
			}
		}
		if !found {
			shallowLines = append(shallowLines, oid)
		}
	}
	for _, oid := range args.shallow {
		if !shallowSet[oid] {
			if _, ok := r.objects[oid]; ok {
				unshallowLines = append(unshallowLines, oid)
			}
		}
	}
	sendShallowInfo := len(shallowLines) > 0 || len(unshallowLines) > 0
	if sendShallowInfo {
		parts = append(parts, pktLine("shallow-info\n"))
		for _, oid := range shallowLines {
			parts = append(parts, pktLine("shallow "+oid+"\n"))
		}
		for _, oid := range unshallowLines {
			parts = append(parts, pktLine("unshallow "+oid+"\n"))
		}
		parts = append(parts, pktFlush)
	}
	if len(args.haves) > 0 || args.done {
		parts = append(parts, pktLine("acknowledgments\n"))
		if len(objects) == 0 || len(args.haves) > 0 {
			parts = append(parts, pktLine("NAK\n"))
		}
		parts = append(parts, pktFlush)
	}
	parts = append(parts, pktLine("packfile\n"))
	pack := buildPack(objects)
	parts = append(parts, sidebandChunks(pack, 65500))
	parts = append(parts, pktFlush)
	size := 0
	for _, p := range parts {
		size += len(p)
	}
	out := make([]byte, 0, size)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func fetchV0Response(r *repo, args fetchArgs) []byte {
	keep, shallow := walkForFetch(r, args.wants, args.depth, args.hasDepth)
	boundaries := map[string]bool{}
	for _, s := range shallow {
		boundaries[s] = true
	}
	objects := objectsForFetch(r, keep, args.haves, boundaries, args.shallow)
	var parts [][]byte
	parts = append(parts, pktLine("NAK\n"))
	pack := buildPack(objects)
	parts = append(parts, sidebandChunks(pack, 65500))
	parts = append(parts, pktFlush)
	size := 0
	for _, p := range parts {
		size += len(p)
	}
	out := make([]byte, 0, size)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// parseLsRefsArgs reads a v2 ls-refs request's argument section.
func parseLsRefsArgs(packets []pkt) (peel, symrefs, unborn bool, prefixes []string) {
	inArgs := false
	for _, p := range packets {
		switch p.kind {
		case pktKindDelim:
			inArgs = true
			continue
		case pktKindFlush:
			return
		case pktKindData:
			if !inArgs {
				continue
			}
			line := strings.TrimSuffix(string(p.payload), "\n")
			switch {
			case line == "peel":
				peel = true
			case line == "symrefs":
				symrefs = true
			case line == "unborn":
				unborn = true
			case strings.HasPrefix(line, "ref-prefix "):
				prefixes = append(prefixes, line[len("ref-prefix "):])
			}
		}
	}
	return
}

// parseFetchArgs reads a v2 fetch request's argument section.
func parseFetchArgs(packets []pkt) fetchArgs {
	args := fetchArgs{}
	inArgs := false
	for _, p := range packets {
		switch p.kind {
		case pktKindDelim:
			inArgs = true
			continue
		case pktKindFlush:
			return args
		case pktKindData:
			if !inArgs {
				continue
			}
			line := strings.TrimSuffix(string(p.payload), "\n")
			switch {
			case strings.HasPrefix(line, "want "):
				args.wants = append(args.wants, line[5:])
			case strings.HasPrefix(line, "have "):
				args.haves = append(args.haves, line[5:])
			case strings.HasPrefix(line, "shallow "):
				args.shallow = append(args.shallow, line[8:])
			case strings.HasPrefix(line, "deepen "):
				args.depth, _ = strconv.Atoi(line[7:])
				args.hasDepth = true
			case line == "done":
				args.done = true
			}
		}
	}
	return args
}

// parseFetchV0Request reads a v0 fetch request's two sections.
func parseFetchV0Request(packets []pkt) fetchArgs {
	args := fetchArgs{}
	section := 0
	for _, p := range packets {
		if p.kind == pktKindFlush {
			section++
			continue
		}
		if p.kind != pktKindData {
			continue
		}
		line := strings.TrimSuffix(string(p.payload), "\n")
		if section == 0 {
			if strings.HasPrefix(line, "want ") {
				rest := line[5:]
				if sp := strings.IndexByte(rest, ' '); sp >= 0 {
					rest = rest[:sp]
				}
				args.wants = append(args.wants, rest)
			} else if strings.HasPrefix(line, "shallow ") {
				args.shallow = append(args.shallow, line[8:])
			} else if strings.HasPrefix(line, "deepen ") {
				args.depth, _ = strconv.Atoi(line[7:])
				args.hasDepth = true
			}
		} else {
			if strings.HasPrefix(line, "have ") {
				args.haves = append(args.haves, line[5:])
			} else if line == "done" {
				args.done = true
			}
		}
	}
	return args
}

// ---------------------------------------------------------------- receive-pack

type pushCommand struct {
	oldOid string
	newOid string
	ref    string
}

// parsePushRequest decodes a receive-pack body: command packets and the raw pack.
func parsePushRequest(b []byte) ([]pushCommand, []byte) {
	packets, trailing := decodePktLines(b)
	var commands []pushCommand
	for _, p := range packets {
		if p.kind != pktKindData {
			continue
		}
		line := strings.TrimSuffix(string(p.payload), "\n")
		if line == "" {
			continue
		}
		if nul := strings.IndexByte(line, 0); nul >= 0 {
			line = line[:nul]
		}
		space := strings.IndexByte(line, ' ')
		if space < 0 {
			continue
		}
		second := strings.IndexByte(line[space+1:], ' ')
		if second < 0 {
			continue
		}
		second += space + 1
		commands = append(commands, pushCommand{
			oldOid: line[:space],
			newOid: line[space+1 : second],
			ref:    line[second+1:],
		})
	}
	return commands, trailing
}

// ingestPack ingests a pack's objects into the repo's store; answers an error line or "" on success.
func (f *forge) ingestPack(r *repo, pack []byte) string {
	if len(pack) == 0 {
		return ""
	}
	read, err := readPack(pack, func(oid string) (gitObject, bool) {
		o, ok := r.objects[oid]
		return o, ok
	})
	if err != nil {
		return err.Error()
	}
	for oid, object := range read.objects {
		if _, ok := r.objects[oid]; !ok {
			r.objects[oid] = object
		}
	}
	return ""
}

// bytesHasLine answers whether data carries `line` as one of its own header lines.
func bytesHasLine(data, line []byte) bool {
	for _, l := range bytesSplitLines(data) {
		if bytes.Equal(l, line) {
			return true
		}
	}
	return false
}

func bytesSplitLines(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' {
			out = append(out, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		out = append(out, data[start:])
	}
	return out
}

// isFastForward answers whether descendant is a fast-forward of ancestor.
func isFastForward(r *repo, ancestor, descendant string) bool {
	if ancestor == descendant {
		return true
	}
	if ancestor == zeroOid {
		return true
	}
	// The common push shape: the new commit's own bytes name its parent on a
	// `parent ` line - a direct child is a fast-forward without a walk.
	if object, ok := r.objects[descendant]; ok && object.typ == typeCommit {
		if bytesHasLine(object.data, []byte("parent "+ancestor)) {
			return true
		}
	}
	seen := map[string]bool{descendant: true}
	queue := []string{descendant}
	for len(queue) > 0 {
		oid := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if oid == ancestor {
			return true
		}
		object, ok := r.objects[oid]
		if !ok || object.typ != typeCommit {
			continue
		}
		c, err := parseCommit(object.data)
		if err != nil {
			continue
		}
		for _, parent := range c.parents {
			if !seen[parent] {
				seen[parent] = true
				queue = append(queue, parent)
			}
		}
	}
	return false
}

// judgeCommand answers a push command's refusal reason or "" when it applies.
func (f *forge) judgeCommand(r *repo, command pushCommand, unpackError string) string {
	if unpackError != "" {
		return "unpacker error"
	}
	current, hasCurrent := r.refs[command.ref]
	currentOid := zeroOid
	if hasCurrent {
		currentOid = current
	}
	if command.oldOid != currentOid {
		if command.oldOid == zeroOid {
			return "already exists"
		}
		return "fetch first"
	}
	if command.newOid == zeroOid {
		return "" // a delete: fine
	}
	if _, ok := r.objects[command.newOid]; !ok {
		return "missing necessary objects"
	}
	if strings.HasPrefix(command.ref, "refs/tags/") {
		return "" // a tag create points at the tag object itself
	}
	if !isFastForward(r, currentOid, command.newOid) {
		return "non-fast-forward"
	}
	return ""
}

func applyPushCommand(r *repo, command pushCommand) {
	if command.newOid == zeroOid {
		delete(r.refs, command.ref)
		return
	}
	r.refs[command.ref] = command.newOid
}

// receivePackResponse applies a push's commands and builds the report-status
// response (in sideband). It answers the payload bytes.
func (f *forge) receivePackResponse(r *repo, body []byte) []byte {
	commands, pack := parsePushRequest(body)
	unpackError := ""
	if pack != nil {
		unpackError = f.ingestPack(r, pack)
	}
	lines := make([]string, 0, len(commands)+1)
	if unpackError == "" {
		lines = append(lines, "unpack ok\n")
	} else {
		lines = append(lines, "unpack "+unpackError+"\n")
	}
	for _, command := range commands {
		if reason := f.judgeCommand(r, command, unpackError); reason == "" {
			applyPushCommand(r, command)
			// A ref moved: the prebuilt shallow packs and advertisements are stale now.
			r.clearGitCaches()
			f.fastGetCache = map[string][]byte{}
			lines = append(lines, "ok "+command.ref+"\n")
		} else {
			lines = append(lines, "ng "+command.ref+" "+reason+"\n")
		}
	}
	buf := make([]byte, reportSizeOf(lines))
	written := 0
	for _, line := range lines {
		written += writePktLine(buf, written, line)
	}
	parts := [][]byte{sidebandChunks(buf, 65500), pktFlush}
	size := 0
	for _, p := range parts {
		size += len(p)
	}
	out := make([]byte, 0, size)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func reportSizeOf(lines []string) int {
	size := 0
	for _, line := range lines {
		size += len(line) + 4
	}
	return size
}

// parseGitPath answers whether the forge's git surface owns this path
// (/{owner}/{repo}.git/...).
func parseGitPath(pathname string) (owner, repoName, rest string, ok bool) {
	if len(pathname) == 0 || pathname[0] != '/' {
		return "", "", "", false
	}
	firstSlash := strings.IndexByte(pathname[1:], '/')
	if firstSlash < 0 {
		return "", "", "", false
	}
	firstSlash++
	owner = pathname[1:firstSlash]
	if owner == "" {
		return "", "", "", false
	}
	remainder := pathname[firstSlash+1:]
	dotGit := strings.Index(remainder, ".git")
	if dotGit < 0 {
		return "", "", "", false
	}
	repoName = remainder[:dotGit]
	if repoName == "" {
		return "", "", "", false
	}
	after := remainder[dotGit+4:]
	if after != "" && after[0] != '/' {
		return "", "", "", false
	}
	if after != "" {
		rest = after[1:]
	}
	return owner, repoName, rest, true
}
