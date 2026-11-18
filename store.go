// The forge's in-memory store: users, repositories, git objects and refs, issues,
// comments, labels and pull requests (store.ts).
package main

import (
	"sort"
	"strconv"
	"strings"
	"sync"
)

type user struct {
	id    int
	login string
	name  string
	email string
}

type label struct {
	id    int
	name  string
	color string
}

type comment struct {
	id          int
	issueNumber int
	body        string
	createdAt   string
	updatedAt   string
	user        string
}

type issue struct {
	id        int
	number    int
	repoID    int
	title     string
	body      string
	state     string // open | closed
	labels    []string
	createdAt string
	updatedAt string
	closedAt  *string
	user      string
	isPull    bool
	pullNumber *int
}

type pullFile struct {
	filename        string
	status          string
	additions       int
	deletions       int
	changes         int
	sha             string
	blobOid         string
	previousBlobOid string
	hasPrevious     bool
}

type pull struct {
	id             int
	number         int
	repoID         int
	title          string
	body           string
	state          string
	merged         bool
	mergedAt       *string
	mergeCommitSha *string
	head           string
	base           string
	headSha        string
	baseSha        string
	createdAt      string
	updatedAt      string
	user           string
	files          []pullFile
	mergeable      string // MERGEABLE | CONFLICTING | UNKNOWN
	issue          *issue
}

type repo struct {
	id               int
	owner            string
	name             string
	description      string
	privateFlag      bool
	defaultBranch    string
	objects          map[string]gitObject
	refs             map[string]string
	head             string
	issues           map[int]*issue
	pulls            map[int]*pull
	labels           map[string]*label // keyed by label id as a decimal string
	comments         map[int]*comment
	nextIssueNumber  int
	nextPullNumber   int
	nextCommentID    int
	requestCounter   int
	issueOrder       []int
	pullOrder        []int
	issueReplyCache  map[int][]byte
	shallowPackCache map[string][]byte
	shallowPackTip   string
	hasShallowTip    bool
	advertCache      map[string][]byte
	lsRefsCache      map[string][]byte
	// comments sorted by id, in the order they were created (commentsOf's order)
}

func newRepo(id int, owner, name, description string, privateFlag bool) *repo {
	return &repo{
		id:               id,
		owner:            owner,
		name:             name,
		description:      description,
		privateFlag:      privateFlag,
		defaultBranch:    "main",
		objects:          map[string]gitObject{},
		refs:             map[string]string{},
		head:             "refs/heads/main",
		issues:           map[int]*issue{},
		pulls:            map[int]*pull{},
		labels:           map[string]*label{},
		comments:         map[int]*comment{},
		nextIssueNumber:  1,
		nextPullNumber:   1,
		nextCommentID:    1,
		issueOrder:       []int{},
		pullOrder:        []int{},
		issueReplyCache:  map[int][]byte{},
		shallowPackCache: map[string][]byte{},
		advertCache:      map[string][]byte{},
		lsRefsCache:      map[string][]byte{},
	}
}

func (r *repo) sortedRefs() [][2]string {
	out := make([][2]string, 0, len(r.refs))
	for name, oid := range r.refs {
		out = append(out, [2]string{name, oid})
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

// clearGitCaches clears every git-surface cache a ref move invalidates.
func (r *repo) clearGitCaches() {
	r.shallowPackCache = map[string][]byte{}
	r.shallowPackTip = ""
	r.hasShallowTip = false
	r.advertCache = map[string][]byte{}
	r.lsRefsCache = map[string][]byte{}
}

type fixedClock struct {
	epochMs int64
	iso     string
}

// fixedClockOf: MAKEFUN_TRIAL_NOW when set and parseable, else 2026-01-01T00:00:00Z.
func fixedClockOf(raw string) *fixedClock {
	epoch := int64(1_767_139_200_000)
	if raw != "" {
		if parsed, err := parseRFC3339(raw); err == nil {
			epoch = parsed
		}
	}
	return &fixedClock{
		epochMs: epoch,
		iso:     rfc3339FromMs(epoch),
	}
}

func (c *fixedClock) nowSec() int64 { return c.epochMs / 1000 }
func (c *fixedClock) isoString() string { return c.iso }

type forge struct {
	users          map[string]*user
	repos          map[string]*repo
	repoOrder      []string // repos in creation order
	clock          *fixedClock
	viewerLogin    string
	fastGetCache   map[string][]byte
	nextUserID     int
	nextRepoID     int
	nextLabelID    int
	requestCounter int
	// mu is the answer path's lock: reads (the git caches' hit paths, GETs) hold
	// it shared; mutations and cache fills hold it exclusive. A clone wave's
	// 50 concurrent requests all read — the wave runs in parallel — while a
	// write (a push, an issue create) still excludes every reader.
	mu sync.RWMutex
}

func newForge(clock *fixedClock, viewerLogin string) *forge {
	f := &forge{
		users:        map[string]*user{},
		repos:        map[string]*repo{},
		clock:        clock,
		viewerLogin:  viewerLogin,
		fastGetCache: map[string][]byte{},
		nextUserID:   2000,
		nextRepoID:   10_000,
		nextLabelID:  500_000,
	}
	for i := range scenarioUsers {
		u := &scenarioUsers[i]
		f.users[u.login] = &user{id: u.id, login: u.login, name: u.name, email: u.email}
	}
	_ = viewerLogin
	return f
}

func (f *forge) requestID() string {
	f.requestCounter++
	return strconv.Itoa(100000 + f.requestCounter)
}

func (f *forge) user(login string) (*user, bool) {
	u, ok := f.users[login]
	return u, ok
}

func (f *forge) ensureUser(login string) *user {
	if u, ok := f.users[login]; ok {
		return u
	}
	u := &user{id: f.nextUserID, login: login, name: login, email: login + "@example.invalid"}
	f.nextUserID++
	f.users[login] = u
	return u
}

func repoKey(owner, name string) string { return strings.ToLower(owner + "/" + name) }

func (f *forge) getRepo(owner, name string) (*repo, bool) {
	r, ok := f.repos[repoKey(owner, name)]
	return r, ok
}

func (f *forge) createRepo(owner, name, description string, privateFlag bool) *repo {
	f.fastGetCache = map[string][]byte{}
	r := newRepo(f.nextRepoID, owner, name, description, privateFlag)
	f.nextRepoID++
	f.repos[repoKey(owner, name)] = r
	f.repoOrder = append(f.repoOrder, repoKey(owner, name))
	return r
}

func (f *forge) deleteRepo(owner, name string) bool {
	f.fastGetCache = map[string][]byte{}
	key := repoKey(owner, name)
	if _, ok := f.repos[key]; !ok {
		return false
	}
	delete(f.repos, key)
	for i, k := range f.repoOrder {
		if k == key {
			f.repoOrder = append(f.repoOrder[:i], f.repoOrder[i+1:]...)
			break
		}
	}
	return true
}

// reposOf answers the repositories owned by login, in creation order.
func (f *forge) reposOf(login string) []*repo {
	var out []*repo
	for _, key := range f.repoOrder {
		if r, ok := f.repos[key]; ok && r.owner == login {
			out = append(out, r)
		}
	}
	return out
}

// firstRepoByID answers the repo whose id decodes to key, scanning the forge's
// repos in map order (graphql.ts's `[...forge.repos.values()].find`).
func (f *forge) repoByIDString(key string) *repo {
	for _, r := range f.repos {
		if strconv.Itoa(r.id) == key {
			return r
		}
	}
	return nil
}

func (f *forge) putObject(r *repo, t gitObjectType, data []byte) string {
	oid := hashObject(t, data)
	if _, ok := r.objects[oid]; !ok {
		r.objects[oid] = gitObject{typ: t, data: data}
	}
	return oid
}

func (f *forge) getObject(r *repo, oid string) (gitObject, bool) {
	o, ok := r.objects[oid]
	return o, ok
}

func oidReacher(objects map[string]gitObject, roots []string) []string {
	seen := map[string]bool{}
	queue := make([]string, 0, len(roots))
	for _, root := range roots {
		if isObjectId(root) {
			queue = append(queue, root)
		}
	}
	var out []string
	for len(queue) > 0 {
		oid := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if seen[oid] {
			continue
		}
		object, ok := objects[oid]
		if !ok {
			continue
		}
		seen[oid] = true
		out = append(out, oid)
		switch object.typ {
		case typeCommit:
			if c, err := parseCommit(object.data); err == nil {
				queue = append(queue, c.tree)
				queue = append(queue, c.parents...)
			}
		case typeTag:
			if target, ok := matchObjectLine(object.data); ok {
				queue = append(queue, target)
			}
		case typeTree:
			if entries, err := parseTree(object.data); err == nil {
				for _, e := range entries {
					if e.mode != "160000" {
						queue = append(queue, e.oid)
					}
				}
			}
		}
	}
	return out
}

func (f *forge) reachable(r *repo, roots []string) []string {
	return oidReacher(r.objects, roots)
}

func (f *forge) moveRef(r *repo, ref, oid string) {
	if oid == zeroOid {
		delete(r.refs, ref)
	} else {
		r.refs[ref] = oid
	}
}

func (f *forge) createIssue(r *repo, title, body string, labels []string, userName string, isPull bool, pullNumber *int) *issue {
	f.fastGetCache = map[string][]byte{}
	number := r.nextIssueNumber
	r.nextIssueNumber++
	iso := f.clock.isoString()
	issueRec := &issue{
		id:        r.id*1000 + number,
		number:    number,
		repoID:    r.id,
		title:     title,
		body:      body,
		state:     "open",
		labels:    append([]string{}, labels...),
		createdAt: iso,
		updatedAt: iso,
		closedAt:  nil,
		user:      userName,
		isPull:    isPull,
		pullNumber: pullNumber,
	}
	r.issues[number] = issueRec
	r.issueOrder = append(r.issueOrder, number)
	return issueRec
}

func (f *forge) createComment(r *repo, issueNumber int, body, userName string) *comment {
	f.fastGetCache = map[string][]byte{}
	issueRec, ok := r.issues[issueNumber]
	if !ok {
		return nil
	}
	c := &comment{
		id:          r.nextCommentID,
		issueNumber: issueNumber,
		body:        body,
		createdAt:   f.clock.isoString(),
		updatedAt:   f.clock.isoString(),
		user:        userName,
	}
	r.nextCommentID++
	r.comments[c.id] = c
	issueRec.updatedAt = f.clock.isoString()
	delete(r.issueReplyCache, issueNumber)
	f.invalidateIssue(r, issueNumber)
	return c
}

// commentsOf answers the issue's comments in id order.
func (f *forge) commentsOf(r *repo, issueNumber int) []*comment {
	var out []*comment
	ids := make([]int, 0, len(r.comments))
	for id, c := range r.comments {
		if c.issueNumber == issueNumber {
			ids = append(ids, id)
		}
	}
	sort.Ints(ids)
	for _, id := range ids {
		out = append(out, r.comments[id])
	}
	return out
}

func (f *forge) createLabel(r *repo, name, color string) *label {
	f.fastGetCache = map[string][]byte{}
	for _, l := range r.labels {
		if l.name == name {
			return l
		}
	}
	l := &label{id: f.nextLabelID, name: name, color: color}
	f.nextLabelID++
	r.labels[strconv.Itoa(l.id)] = l
	return l
}

func (f *forge) labelByName(r *repo, name string) *label {
	for _, l := range r.labels {
		if l.name == name {
			return l
		}
	}
	return nil
}

func (f *forge) createPull(r *repo, title, body, head, base, headSha, baseSha, userName string) *pull {
	f.fastGetCache = map[string][]byte{}
	number := r.nextPullNumber
	r.nextPullNumber++
	iso := f.clock.isoString()
	issueRec := f.createIssue(r, title, body, []string{}, userName, true, &number)
	p := &pull{
		id:        r.id*10_000 + number,
		number:    number,
		repoID:    r.id,
		title:     title,
		body:      body,
		state:     "open",
		head:      head,
		base:      base,
		headSha:   headSha,
		baseSha:   baseSha,
		createdAt: iso,
		updatedAt: iso,
		user:      userName,
		files:     []pullFile{},
		mergeable: "MERGEABLE",
		issue:     issueRec,
	}
	r.pulls[number] = p
	r.pullOrder = append(r.pullOrder, number)
	return p
}

func countLines(b []byte) int {
	lines := 0
	for _, c := range b {
		if c == 0x0a {
			lines++
		}
	}
	if len(b) > 0 && b[len(b)-1] != 0x0a {
		lines++
	}
	return lines
}

// treeOf answers the flat map path -> blob oid a commit's tree holds (gitlinks skipped).
func (f *forge) treeOf(r *repo, commitSha string) map[string]string {
	out := map[string]string{}
	object, ok := r.objects[commitSha]
	if !ok || object.typ != typeCommit {
		return out
	}
	c, err := parseCommit(object.data)
	if err != nil {
		return out
	}
	var walk func(treeOid, prefix string)
	walk = func(treeOid, prefix string) {
		tree, ok := r.objects[treeOid]
		if !ok || tree.typ != typeTree {
			return
		}
		entries, err := parseTree(tree.data)
		if err != nil {
			return
		}
		for _, e := range entries {
			path := e.name
			if prefix != "" {
				path = prefix + "/" + e.name
			}
			if e.mode == "40000" {
				walk(e.oid, path)
			} else if e.mode != "160000" {
				out[path] = e.oid
			}
		}
	}
	walk(c.tree, "")
	return out
}

// diffFiles answers the changed files between a pull's head and base commits.
func (f *forge) diffFiles(r *repo, baseSha, headSha string) []pullFile {
	baseTree := f.treeOf(r, baseSha)
	headTree := f.treeOf(r, headSha)
	paths := make([]string, 0, len(baseTree)+len(headTree))
	for path := range baseTree {
		paths = append(paths, path)
	}
	for path := range headTree {
		if _, ok := baseTree[path]; !ok {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	empty := []byte{}
	var files []pullFile
	for _, path := range paths {
		before, hasBefore := baseTree[path]
		after, hasAfter := headTree[path]
		if hasBefore && hasAfter && before == after {
			continue
		}
		beforeBytes := empty
		if hasBefore {
			if o, ok := r.objects[before]; ok {
				beforeBytes = o.data
			}
		}
		afterBytes := empty
		if hasAfter {
			if o, ok := r.objects[after]; ok {
				afterBytes = o.data
			}
		}
		if bytesEqual(beforeBytes, afterBytes) {
			continue
		}
		beforeLines := 0
		if hasBefore {
			beforeLines = countLines(beforeBytes)
		}
		afterLines := 0
		if hasAfter {
			afterLines = countLines(afterBytes)
		}
		status := "modified"
		if !hasBefore {
			status = "added"
		} else if !hasAfter {
			status = "removed"
		}
		additions := afterLines
		deletions := beforeLines
		if status == "removed" {
			additions = 0
		}
		if status == "added" {
			deletions = 0
		}
		sha := before
		if hasAfter {
			sha = after
		} else if !hasBefore {
			sha = ""
		}
		files = append(files, pullFile{
			filename:  path,
			status:    status,
			additions: additions,
			deletions: deletions,
			changes:   additions + deletions,
			sha:       sha,
			blobOid:   after,
			previousBlobOid: before,
			hasPrevious:    hasBefore,
		})
	}
	return files
}
