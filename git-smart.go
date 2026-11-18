// Git smart HTTP: upload-pack (protocol v2 and v0) and receive-pack (v0),
// served at /{owner}/{repo}.git/info/refs, /git-upload-pack, /git-receive-pack
// (git-smart.ts).
package main

import (
	"strconv"
	"strings"
)

// ---------------------------------------------------------------- pkt-line

// writePktLine writes one pkt-line into buffer at offset; answers the bytes written.
func writePktLine(buffer []byte, offset int, payload string) int {
	length := len(payload) + 4
	buffer[offset] = hexDigits[(length>>12)&0xf]
	buffer[offset+1] = hexDigits[(length>>8)&0xf]
	buffer[offset+2] = hexDigits[(length>>4)&0xf]
	buffer[offset+3] = hexDigits[length&0xf]
	copy(buffer[offset+4:], payload)
	return length
}

func pktLine(payload string) []byte {
	out := make([]byte, 0, len(payload)+4)
	return append(out, buildPktLine(payload)...)
}

func buildPktLine(payload string) []byte {
	length := len(payload) + 4
	out := make([]byte, 4+len(payload))
	out[0] = hexDigits[(length>>12)&0xf]
	out[1] = hexDigits[(length>>8)&0xf]
	out[2] = hexDigits[(length>>4)&0xf]
	out[3] = hexDigits[length&0xf]
	copy(out[4:], payload)
	return out
}

// pktBytes frames a payload that is not valid UTF-8 text as a packet.
func pktBytes(payload []byte) []byte {
	length := len(payload) + 4
	out := make([]byte, 4+len(payload))
	out[0] = hexDigits[(length>>12)&0xf]
	out[1] = hexDigits[(length>>8)&0xf]
	out[2] = hexDigits[(length>>4)&0xf]
	out[3] = hexDigits[length&0xf]
	copy(out[4:], payload)
	return out
}

var pktFlush = []byte("0000")
var pktDelim = []byte("0001")

// ---------------------------------------------------------------- request decoding

type pktKind int

const (
	pktKindData pktKind = iota
	pktKindFlush
	pktKindDelim
	pktKindResponseEnd
)

type pkt struct {
	kind    pktKind
	payload []byte
}

// decodePktLines decodes a complete pkt-line stream.
func decodePktLines(b []byte) ([]pkt, []byte) {
	var packets []pkt
	offset := 0
	for offset+4 <= len(b) {
		hexOK := true
		for _, c := range b[offset : offset+4] {
			if !isHexByte(c) {
				hexOK = false
				break
			}
		}
		if !hexOK {
			break
		}
		length, err := strconv.ParseUint(string(b[offset:offset+4]), 16, 32)
		if err != nil {
			break
		}
		n := int(length)
		if n == 0 {
			packets = append(packets, pkt{kind: pktKindFlush})
			offset += 4
			continue
		}
		if n == 1 {
			packets = append(packets, pkt{kind: pktKindDelim})
			offset += 4
			continue
		}
		if n == 2 {
			packets = append(packets, pkt{kind: pktKindResponseEnd})
			offset += 4
			continue
		}
		if n < 4 || offset+n > len(b) {
			break
		}
		packets = append(packets, pkt{kind: pktKindData, payload: b[offset+4 : offset+n]})
		offset += n
	}
	return packets, b[offset:]
}

func isHexByte(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// ---------------------------------------------------------------- sideband

// sidebandChunks frames payload as sideband band-1 packets.
func sidebandChunks(payload []byte, chunkSize int) []byte {
	if chunkSize <= 0 {
		chunkSize = 65_500
	}
	var out []byte
	for offset := 0; offset < len(payload); offset += chunkSize {
		end := offset + chunkSize
		if end > len(payload) {
			end = len(payload)
		}
		slice := payload[offset:end]
		frame := make([]byte, len(slice)+1)
		frame[0] = 1
		copy(frame[1:], slice)
		out = append(out, pktBytes(frame)...)
	}
	return out
}

// ---------------------------------------------------------------- advertisements

const gitAgent = "forge-core/1.0"

type advertisedRef struct {
	oid          string
	name         string
	peeled       string
	hasPeeled    bool
	symrefTarget string
	hasSymref    bool
}

// refsForAdvertisement answers HEAD first (a symref), then every ref sorted.
func refsForAdvertisement(r *repo) []advertisedRef {
	var out []advertisedRef
	if headTarget, ok := r.refs[r.head]; ok {
		out = append(out, advertisedRef{oid: headTarget, name: "HEAD", symrefTarget: r.head, hasSymref: true})
	}
	for _, ref := range r.sortedRefs() {
		entry := advertisedRef{oid: ref[1], name: ref[0]}
		if strings.HasPrefix(ref[0], "refs/tags/") {
			if object, ok := r.objects[ref[1]]; ok && object.typ == typeTag {
				entry.peeled = peelTo(r.objects, ref[1])
				entry.hasPeeled = true
			}
		}
		out = append(out, entry)
	}
	return out
}

func uploadPackV2Advertisement() []byte {
	var out []byte
	for _, line := range []string{
		"version 2\n",
		"agent=" + gitAgent + "\n",
		"ls-refs=unborn\n",
		"fetch=shallow filter thin-pack ofs-delta\n",
		"server-option\n",
		"object-format=sha1\n",
	} {
		out = append(out, pktLine(line)...)
	}
	return out
}

func lsRefsResponse(r *repo, peel, symrefs, unborn bool, prefixes []string) []byte {
	// `unborn` is parsed and accepted but only colours an empty repo's answer,
	// which the v0/v2 shape below already covers.
	_ = unborn
	keep := func(name string) bool {
		if len(prefixes) == 0 {
			return true
		}
		for _, prefix := range prefixes {
			if strings.HasPrefix(name, prefix) {
				return true
			}
		}
		return false
	}
	var lines []string
	for _, ref := range refsForAdvertisement(r) {
		if !keep(ref.name) {
			continue
		}
		line := ref.oid + " " + ref.name
		if ref.name == "HEAD" && symrefs && ref.hasSymref {
			line += " symref-target:" + ref.symrefTarget
		}
		if peel && ref.hasPeeled {
			line += " peeled:" + ref.peeled
		}
		lines = append(lines, line+"\n")
	}
	if len(lines) == 0 {
		return append([]byte{}, pktFlush...)
	}
	var out []byte
	for _, line := range lines {
		out = append(out, pktLine(line)...)
	}
	out = append(out, pktFlush...)
	return out
}

func uploadPackV0Advertisement(r *repo, withPreamble bool) []byte {
	refs := refsForAdvertisement(r)
	capabilities := "multi_ack_detailed multi_ack side-band-64k thin-pack ofs-delta shallow deepen-since deepen-not agent=" + gitAgent
	var parts [][]byte
	if withPreamble {
		parts = append(parts, pktLine("# service=git-upload-pack\n"), pktFlush)
	}
	var head *advertisedRef
	for i := range refs {
		if refs[i].name == "HEAD" {
			head = &refs[i]
			break
		}
	}
	var first *advertisedRef
	if head != nil {
		first = head
	} else if len(refs) > 0 {
		first = &refs[0]
	}
	if first == nil {
		parts = append(parts, pktLine(zeroOid+" capabilities^{}\u0000"+capabilities+"\n"))
	} else {
		// Emit the first line with capabilities, then the rest in order.
		parts = append(parts, pktLine(first.oid+" "+first.name+"\u0000"+capabilities+"\n"))
		for i := range refs {
			ref := &refs[i]
			if ref == first {
				continue
			}
			parts = append(parts, pktLine(ref.oid+" "+ref.name+"\n"))
			if ref.hasPeeled {
				parts = append(parts, pktLine(ref.peeled+" "+ref.name+"^{}\n"))
			}
		}
	}
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
