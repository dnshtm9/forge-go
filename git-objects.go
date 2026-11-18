// The git object model: SHA-1 object hashing exactly as git computes it, commit and tree
// parsing and building, and the packfile read/write paths (git-objects.ts).
package main

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
)

const zeroOid = "0000000000000000000000000000000000000000"

type gitObjectType string

const (
	typeCommit gitObjectType = "commit"
	typeTree   gitObjectType = "tree"
	typeBlob   gitObjectType = "blob"
	typeTag    gitObjectType = "tag"
)

func codeOf(t gitObjectType) byte {
	switch t {
	case typeCommit:
		return 1
	case typeTree:
		return 2
	case typeBlob:
		return 3
	case typeTag:
		return 4
	}
	return 0
}

func typeOfCode(c byte) (gitObjectType, bool) {
	switch c {
	case 1:
		return typeCommit, true
	case 2:
		return typeTree, true
	case 3:
		return typeBlob, true
	case 4:
		return typeTag, true
	}
	return "", false
}

type gitObject struct {
	typ  gitObjectType
	data []byte
}

func objectHeader(t gitObjectType, size int) []byte {
	b := make([]byte, 0, 24)
	b = append(b, t...)
	b = append(b, ' ')
	b = append(b, strconv.Itoa(size)...)
	b = append(b, 0)
	return b
}

func hashObject(t gitObjectType, data []byte) string {
	h := sha1.New()
	h.Write(objectHeader(t, len(data)))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

var hexRunes = "0123456789abcdef"

func isObjectId(s string) bool {
	if len(s) != 40 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

type treeEntry struct {
	mode string
	name string
	oid  string
}

func parseTree(data []byte) ([]treeEntry, error) {
	var entries []treeEntry
	off := 0
	for off < len(data) {
		sp := bytes.IndexByte(data[off:], ' ')
		if sp < 0 {
			return nil, errors.New("tree entry has no mode separator")
		}
		sp += off
		nul := bytes.IndexByte(data[sp+1:], 0)
		if nul < 0 {
			return nil, errors.New("tree entry has no name terminator")
		}
		nul += sp + 1
		if nul+21 > len(data) {
			return nil, errors.New("tree entry has a truncated object id")
		}
		entries = append(entries, treeEntry{
			mode: string(data[off:sp]),
			name: string(data[sp+1 : nul]),
			oid:  hex.EncodeToString(data[nul+1 : nul+21]),
		})
		off = nul + 21
	}
	return entries, nil
}

// gitTreeNameOrder compares two tree names the way git sorts them: byte order of
// the name, a directory compared as if it ended in '/'.
func gitTreeNameOrder(a, b treeEntry) bool {
	an := a.name
	bn := b.name
	if a.mode == "40000" {
		an += "/"
	}
	if b.mode == "40000" {
		bn += "/"
	}
	return an < bn
}

func buildTree(entries []treeEntry) ([]byte, error) {
	sorted := make([]treeEntry, len(entries))
	copy(sorted, entries)
	// Stable sort in git's order (sort.SliceStable mirrors Array.prototype.sort
	// with a comparator on the same key set).
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && gitTreeNameOrder(sorted[j], sorted[j-1]); j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	var out []byte
	for _, e := range sorted {
		if !isObjectId(e.oid) {
			return nil, fmt.Errorf("tree entry %s has a bad object id", e.name)
		}
		out = append(out, e.mode...)
		out = append(out, ' ')
		out = append(out, e.name...)
		out = append(out, 0)
		raw, err := hex.DecodeString(e.oid)
		if err != nil {
			return nil, err
		}
		out = append(out, raw...)
	}
	return out, nil
}

type commitView struct {
	tree      string
	parents   []string
	author    string
	committer string
	message   string
}

func parseCommit(data []byte) (commitView, error) {
	var view commitView
	head := data
	if i := bytes.Index(data, []byte("\n\n")); i >= 0 {
		head = data[:i]
		view.message = string(data[i+2:])
	}
	for _, line := range bytes.Split(head, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		sp := bytes.IndexByte(line, ' ')
		var key, value string
		if sp < 0 {
			key = string(line)
		} else {
			key = string(line[:sp])
			value = string(line[sp+1:])
		}
		switch key {
		case "tree":
			view.tree = value
		case "parent":
			view.parents = append(view.parents, value)
		case "author":
			view.author = value
		case "committer":
			view.committer = value
		}
	}
	if !isObjectId(view.tree) {
		return view, errors.New("commit has no valid tree header")
	}
	return view, nil
}

// matchObjectLine finds `^object <40 hex>` in a tag body (a tag's target).
func matchObjectLine(data []byte) (string, bool) {
	for _, line := range bytes.Split(data, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("object ")) && len(line) >= 47 && isObjectId(string(line[7:47])) {
			return string(line[7:47]), true
		}
		if len(line) == 0 {
			break // the header block ended
		}
	}
	return "", false
}

func peelTo(objects map[string]gitObject, oid string) string {
	current := oid
	for depth := 0; depth < 10; depth++ {
		object, ok := objects[current]
		if !ok || object.typ != typeTag {
			return current
		}
		next, ok := matchObjectLine(object.data)
		if !ok {
			return current
		}
		current = next
	}
	return current
}

// ---------------------------------------------------------------- inflate

// inflateAt inflates one zlib stream at offset; returns its bytes and the
// offset just past the stream.
func inflateAt(buf []byte, off, end int) ([]byte, int, error) {
	br := bytes.NewReader(buf[off:end])
	zr, err := zlib.NewReader(br)
	if err != nil {
		return nil, 0, err
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		zr.Close()
		return nil, 0, err
	}
	if err := zr.Close(); err != nil {
		return nil, 0, err
	}
	consumed := (end - off) - br.Len()
	return out, off + consumed, nil
}

// deflateLevel1 deflates data at zlib level 1 (matching deflateSync level 1).
func deflateLevel1(data []byte) []byte {
	var buf bytes.Buffer
	zw, _ := zlib.NewWriterLevel(&buf, zlib.BestSpeed)
	zw.Write(data)
	zw.Close()
	return buf.Bytes()
}

// ---------------------------------------------------------------- pack read

type rawEntry struct {
	offset     int
	typeCode   byte
	size       int
	data       []byte
	baseOffset int
	baseOid    string
	resolved   []byte
	resType    gitObjectType
	oid        string
}

func readVarintSize(buf []byte, start int) (size int, next int, first byte, err error) {
	if start >= len(buf) {
		return 0, 0, 0, errors.New("pack entry has no size header")
	}
	b := buf[start]
	first = b
	size = int(b & 0x0f)
	shift := uint(4)
	offset := start + 1
	for b&0x80 != 0 {
		if offset >= len(buf) {
			return 0, 0, 0, errors.New("pack entry size varint is truncated")
		}
		b = buf[offset]
		offset++
		size += int(b&0x7f) << shift
		shift += 7
		if shift > 63 {
			return 0, 0, 0, errors.New("pack entry size varint is too long")
		}
	}
	return size, offset, first, nil
}

func readOfsDeltaBase(buf []byte, start int) (baseOffset int, next int, err error) {
	if start >= len(buf) {
		return 0, 0, errors.New("ofs-delta has no base offset")
	}
	b := buf[start]
	offset := start + 1
	value := int(b & 0x7f)
	for b&0x80 != 0 {
		if offset >= len(buf) {
			return 0, 0, errors.New("ofs-delta base offset is truncated")
		}
		b = buf[offset]
		offset++
		value = (value+1)*128 + int(b&0x7f)
	}
	return value, offset, nil
}

func applyDelta(base, delta []byte) ([]byte, error) {
	off := 0
	readSize := func() (int, error) {
		value, shift := 0, uint(0)
		for {
			if off >= len(delta) {
				return 0, errors.New("delta size varint is truncated")
			}
			b := delta[off]
			off++
			value += int(b&0x7f) << shift
			shift += 7
			if shift > 63 {
				return 0, errors.New("delta size varint is too long")
			}
			if b&0x80 == 0 {
				return value, nil
			}
		}
	}
	sourceSize, err := readSize()
	if err != nil {
		return nil, err
	}
	targetSize, err := readSize()
	if err != nil {
		return nil, err
	}
	if sourceSize != len(base) {
		return nil, fmt.Errorf("delta expects a %d-byte base, got %d", sourceSize, len(base))
	}
	out := make([]byte, 0, targetSize)
	written := 0
	for off < len(delta) {
		op := delta[off]
		off++
		if op&0x80 != 0 {
			copyOffset, copySize, shift := 0, 0, uint(0)
			for bit := 0; bit < 4; bit++ {
				if op&(1<<bit) != 0 {
					if off >= len(delta) {
						return nil, errors.New("delta copy offset is truncated")
					}
					copyOffset += int(delta[off]) << shift
					shift += 8
					off++
				} else {
					shift += 8
				}
			}
			shift = 0
			for bit := 4; bit < 7; bit++ {
				if op&(1<<bit) != 0 {
					if off >= len(delta) {
						return nil, errors.New("delta copy size is truncated")
					}
					copySize += int(delta[off]) << shift
					shift += 8
					off++
				} else {
					shift += 8
				}
			}
			if copySize == 0 {
				copySize = 0x10000
			}
			if copyOffset+copySize > len(base) {
				return nil, errors.New("delta copy is out of the base")
			}
			if written+copySize > targetSize {
				return nil, errors.New("delta writes past its target size")
			}
			out = append(out, base[copyOffset:copyOffset+copySize]...)
			written += copySize
		} else if op != 0 {
			literal := int(op & 0x7f)
			if off+literal > len(delta) {
				return nil, errors.New("delta insert is truncated")
			}
			if written+literal > targetSize {
				return nil, errors.New("delta writes past its target size")
			}
			out = append(out, delta[off:off+literal]...)
			off += literal
			written += literal
		} else {
			return nil, errors.New("delta opcode 0 is invalid")
		}
	}
	if written != targetSize {
		return nil, fmt.Errorf("delta produced %d of %d bytes", written, targetSize)
	}
	return out, nil
}

type packRead struct {
	version       uint32
	declaredCount int
	objects       map[string]gitObject
	deltaCount    int
}

func readPack(pack []byte, resolveBase func(string) (gitObject, bool)) (packRead, error) {
	if len(pack) < 32 {
		return packRead{}, fmt.Errorf("pack is %d bytes, too short", len(pack))
	}
	if string(pack[:4]) != "PACK" {
		return packRead{}, errors.New("pack does not start with PACK")
	}
	version := binary.BigEndian.Uint32(pack[4:8])
	if version != 2 && version != 3 {
		return packRead{}, fmt.Errorf("unsupported pack version %d", version)
	}
	declaredCount := int(binary.BigEndian.Uint32(pack[8:12]))
	trailerStart := len(pack) - 20

	raw := []*rawEntry{}
	byOffset := map[int]*rawEntry{}
	offset := 12
	sawDelta := false
	for index := 0; index < declaredCount; index++ {
		entryOffset := offset
		size, next, first, err := readVarintSize(pack, offset)
		if err != nil {
			return packRead{}, err
		}
		offset = next
		typeCode := (first >> 4) & 0x07
		entry := &rawEntry{offset: entryOffset, typeCode: typeCode, size: size}
		if typeCode == 6 {
			sawDelta = true
			base, n2, err := readOfsDeltaBase(pack, offset)
			if err != nil {
				return packRead{}, err
			}
			offset = n2
			if base <= 0 || base > entryOffset {
				return packRead{}, fmt.Errorf("ofs-delta at %d names an invalid base offset %d", entryOffset, base)
			}
			entry.baseOffset = entryOffset - base
		} else if typeCode == 7 {
			sawDelta = true
			if offset+20 > trailerStart {
				return packRead{}, errors.New("ref-delta base id is truncated")
			}
			entry.baseOid = hex.EncodeToString(pack[offset : offset+20])
			offset += 20
		} else if _, ok := typeOfCode(typeCode); !ok {
			return packRead{}, fmt.Errorf("pack entry %d has unknown type code %d", index, typeCode)
		}
		inflated, n3, err := inflateAt(pack, offset, trailerStart)
		if err != nil {
			return packRead{}, err
		}
		if len(inflated) != size {
			return packRead{}, fmt.Errorf("pack entry at %d declares %d bytes but inflated to %d", entryOffset, size, len(inflated))
		}
		entry.data = inflated
		offset = n3
		raw = append(raw, entry)
		byOffset[entryOffset] = entry
	}
	if offset != trailerStart {
		return packRead{}, fmt.Errorf("pack has %d stray bytes before its trailer", trailerStart-offset)
	}

	objects := map[string]gitObject{}
	if !sawDelta {
		for _, entry := range raw {
			typ, _ := typeOfCode(entry.typeCode)
			oid := hashObject(typ, entry.data)
			if _, ok := objects[oid]; !ok {
				objects[oid] = gitObject{typ: typ, data: entry.data}
			}
		}
		return packRead{version: version, declaredCount: declaredCount, objects: objects}, nil
	}

	resolving := map[int]bool{}
	resolvedByOid := map[string]*rawEntry{}
	var resolve func(entry *rawEntry) (gitObjectType, gitObject, error)
	resolve = func(entry *rawEntry) (gitObjectType, gitObject, error) {
		if entry.oid != "" {
			return entry.resType, gitObject{typ: entry.resType, data: entry.resolved}, nil
		}
		if resolving[entry.offset] {
			return "", gitObject{}, fmt.Errorf("delta cycle at pack offset %d", entry.offset)
		}
		resolving[entry.offset] = true
		var typ gitObjectType
		var data []byte
		if entry.typeCode == 6 {
			baseEntry, ok := byOffset[entry.baseOffset]
			if !ok {
				return "", gitObject{}, fmt.Errorf("ofs-delta base at offset %d is not in the pack", entry.baseOffset)
			}
			bt, bobj, err := resolve(baseEntry)
			if err != nil {
				return "", gitObject{}, err
			}
			typ = bt
			data, err = applyDelta(bobj.data, entry.data)
			if err != nil {
				return "", gitObject{}, err
			}
		} else if entry.typeCode == 7 {
			baseEntry, ok := resolvedByOid[entry.baseOid]
			if ok {
				bt, bobj, err := resolve(baseEntry)
				if err != nil {
					return "", gitObject{}, err
				}
				typ = bt
				data, err = applyDelta(bobj.data, entry.data)
				if err != nil {
					return "", gitObject{}, err
				}
			} else {
				external, ok := resolveBase(entry.baseOid)
				if !ok {
					return "", gitObject{}, fmt.Errorf("ref-delta base %s is neither in the pack nor supplied", entry.baseOid)
				}
				typ = external.typ
				var derr error
				data, derr = applyDelta(external.data, entry.data)
				if derr != nil {
					return "", gitObject{}, derr
				}
			}
		} else {
			typ, _ = typeOfCode(entry.typeCode)
			data = entry.data
		}
		oid := hashObject(typ, data)
		entry.resolved = data
		entry.resType = typ
		entry.oid = oid
		resolvedByOid[oid] = entry
		delete(resolving, entry.offset)
		return typ, gitObject{typ: typ, data: data}, nil
	}

	deltaCount := 0
	for _, entry := range raw {
		_, object, err := resolve(entry)
		if err != nil {
			return packRead{}, err
		}
		if entry.typeCode == 6 || entry.typeCode == 7 {
			deltaCount++
		}
		if _, ok := objects[entry.oid]; !ok {
			objects[entry.oid] = object
		}
	}
	return packRead{version: version, declaredCount: declaredCount, objects: objects, deltaCount: deltaCount}, nil
}

// ---------------------------------------------------------------- pack write

// packEntry is one object a built pack carries.
type packEntryIn struct {
	typ  gitObjectType
	data []byte
}

func packObjectHeader(t gitObjectType, size int) []byte {
	code := codeOf(t)
	var bytes []byte
	remaining := size
	first := (code << 4) | byte(remaining&0x0f)
	remaining >>= 4
	for remaining > 0 {
		bytes = append(bytes, first|0x80)
		first = byte(remaining & 0x7f)
		remaining >>= 7
	}
	bytes = append(bytes, first)
	return bytes
}

func writePack(objects []packEntryIn) []byte {
	var body bytes.Buffer
	body.WriteString("PACK")
	binary.Write(&body, binary.BigEndian, uint32(2))
	binary.Write(&body, binary.BigEndian, uint32(len(objects)))
	for _, o := range objects {
		body.Write(packObjectHeader(o.typ, len(o.data)))
		body.Write(deflateLevel1(o.data))
	}
	sum := sha1.Sum(body.Bytes())
	body.Write(sum[:])
	return body.Bytes()
}
