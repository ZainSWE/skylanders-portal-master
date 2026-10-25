package main

// Recognising figures on the portal. The catalogue, element rules, villain
// list and save-data layout are ported from skylanders-portal (MIT License,
// Copyright (c) 2026 TheBiemGamer). Everything here only reads figures.

import (
	"crypto/aes"
	"crypto/md5"
	"strings"
	"unicode"
)

type catalogEntry struct {
	id, variant          uint16
	name, game, category string
}

type trapVillain struct {
	id      uint8
	name    string
	trapID  uint16
	variant bool
}

// FigureInfo is what the app knows about a figure on the portal.
type FigureInfo struct {
	ID        uint16 `json:"id"`
	Variant   uint16 `json:"variant"`
	Name      string `json:"name"`
	Character string `json:"character"` // base character ("Chop Chop" for "Legendary Chop Chop")
	Game      string `json:"game"`
	Kind      string `json:"kind"`    // "Trap Master", "Trap", "Giant", ...
	Element   string `json:"element"` // "Magic", "Fire", ... or ""
	Villain   string `json:"villain"` // for traps: the villain inside, "" if empty/unknown
	IsTrap    bool   `json:"isTrap"`
	Reading   bool   `json:"reading"` // still reading the trap's villain
	Slot      int    `json:"slot"`
	Arrival   int    `json:"arrival"` // increases each time a figure is placed
	Unknown   bool   `json:"unknown"`
	trapData  []byte
}

func displayName(s string) string { return strings.ReplaceAll(s, "_s", "'s") }

func normalizeName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func findCatalog(id, variant uint16) (catalogEntry, bool) {
	var fallback *catalogEntry
	for i := range figureCatalog {
		e := &figureCatalog[i]
		if e.id != id {
			continue
		}
		if e.variant == variant {
			return *e, true
		}
		if fallback == nil || e.variant == 0 {
			fallback = e
		}
	}
	if fallback != nil {
		return *fallback, true
	}
	return catalogEntry{}, false
}

var crystalElements = map[uint16]string{680: "Magic", 681: "Water", 682: "Air", 683: "Undead", 684: "Tech",
	685: "Fire", 686: "Earth", 687: "Life", 688: "Dark", 689: "Light"}

var trapElements = map[uint16]string{210: "Magic", 211: "Water", 212: "Air", 213: "Undead", 214: "Tech",
	215: "Fire", 216: "Earth", 217: "Life", 218: "Dark", 219: "Light", 220: "Kaos"}

// characterOf finds the base character a figure belongs to, and its element.
func characterOf(name string) (character, element string) {
	n := normalizeName(name)
	best := 0
	for _, c := range characterElements {
		base := normalizeName(c.name)
		if len(base) > best && strings.Contains(n, base) {
			character, element, best = c.name, c.element, len(base)
		}
	}
	return character, element
}

func entryElement(e catalogEntry) string {
	switch e.category {
	case "Creation Crystals":
		return crystalElements[e.id]
	case "Traps":
		return trapElements[e.id]
	case "Items", "Unreleased":
		return ""
	}
	_, el := characterOf(e.name)
	return el
}

func kindOf(e catalogEntry) string {
	if strings.Contains(e.name, "(Series ") {
		i := strings.Index(e.name, "(Series ")
		if i+8 < len(e.name) {
			return "Series " + string(e.name[i+8])
		}
	}
	switch e.category {
	case "Cores":
		return "Skylander"
	case "LightCores":
		return "LightCore Skylander"
	case "Giants":
		return "Giant"
	case "Sidekicks":
		return "Sidekick"
	case "Swappers":
		return "Swapper"
	case "Items":
		return "Magic Item"
	case "Minis":
		return "Mini"
	case "Traps":
		return "Trap"
	case "Trap Masters":
		return "Trap Master"
	case "Vehicles":
		return "Vehicle"
	case "Senseis":
		return "Sensei"
	case "Villain Senseis":
		return "Villain Sensei"
	case "Creation Crystals":
		return "Creation Crystal"
	case "Variants", "Chase Variants", "Vehicle Variants", "Sensei Variants", "Villain Variants":
		return "Variant"
	}
	return strings.TrimSuffix(e.category, "s")
}

// Identify turns a figure's first two blocks (32 bytes) into what it is.
func Identify(first32 []byte) FigureInfo {
	if len(first32) < 32 {
		return FigureInfo{Name: "Unknown figure", Unknown: true}
	}
	id := uint16(first32[0x10]) | uint16(first32[0x11])<<8
	variant := uint16(first32[0x1C]) | uint16(first32[0x1D])<<8
	e, ok := findCatalog(id, variant)
	if !ok {
		return FigureInfo{ID: id, Variant: variant, Name: "Unknown figure", Unknown: true}
	}
	name := displayName(e.name)
	if e.category == "Traps" { // "Fire Torch (Spark Spear)": the name in brackets is the trap's own
		if i := strings.Index(name, "("); i > 0 && strings.HasSuffix(name, ")") {
			name = name[i+1 : len(name)-1]
		}
	}
	f := FigureInfo{ID: id, Variant: variant, Name: name, Game: e.game, Kind: kindOf(e),
		Element: entryElement(e), IsTrap: e.category == "Traps"}
	switch e.category {
	case "Traps", "Items", "Unreleased", "Creation Crystals":
	default:
		f.Character, _ = characterOf(e.name)
	}
	return f
}

// ---- trap save data ----

var figureKeyConstant = []byte(" Copyright (C) 2010 Activision. All Rights Reserved. ")

// decryptBlock decrypts one 16-byte block of a figure; first32 is the figure's first two blocks.
func decryptBlock(first32 []byte, index uint8, enc []byte) []byte {
	material := make([]byte, 0, 0x21+len(figureKeyConstant))
	material = append(material, first32[:0x20]...)
	material = append(material, index)
	material = append(material, figureKeyConstant...)
	key := md5.Sum(material)
	c, _ := aes.NewCipher(key[:])
	out := make([]byte, 16)
	c.Decrypt(out, enc)
	return out
}

func encryptBlock(first32 []byte, index uint8, plain []byte) []byte {
	material := append(append(append([]byte{}, first32[:0x20]...), index), figureKeyConstant...)
	key := md5.Sum(material)
	c, _ := aes.NewCipher(key[:])
	out := make([]byte, 16)
	c.Encrypt(out, plain)
	return out
}

func figureCRC16(b []byte) uint16 {
	crc := uint16(0xFFFF)
	for _, v := range b {
		crc ^= uint16(v) << 8
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// trapAreaBlocks are the block numbers of a trap's two save areas (skipping sector trailers).
func trapAreaBlocks(area int) []uint8 {
	var out []uint8
	b := 8
	if area == 1 {
		b = 36
	}
	for ; len(out) < 21; b++ {
		if b%4 != 3 {
			out = append(out, uint8(b))
		}
	}
	return out
}

// TrapBlocksToRead lists every block needed to find a trap's villain.
func TrapBlocksToRead() []uint8 {
	return append(append([]uint8{0, 1}, trapAreaBlocks(0)...), trapAreaBlocks(1)...)
}

// ReadTrapVillain finds the villain inside a trap from its blocks (block number -> 16 bytes).
// ok is false if the save data can't be read; name is "" for an empty trap.
func ReadTrapVillain(trapID uint16, blocks map[uint8][]byte) (name string, ok bool) {
	b0, b1 := blocks[0], blocks[1]
	if len(b0) != 16 || len(b1) != 16 {
		return "", false
	}
	first32 := append(append([]byte{}, b0...), b1...)
	read := func(area int) (data []byte, blank, valid bool) {
		blank = true
		for _, idx := range trapAreaBlocks(area) {
			enc := blocks[idx]
			if len(enc) != 16 {
				return nil, false, false
			}
			for _, v := range enc {
				if v != 0 {
					blank = false
				}
			}
			data = append(data, decryptBlock(first32, idx, enc)...)
		}
		u16 := func(at int) uint16 { return uint16(data[at]) | uint16(data[at+1])<<8 }
		head := append([]byte{}, data[:16]...)
		head[14], head[15] = 0x05, 0x00
		valid = u16(14) == figureCRC16(head) && u16(12) == figureCRC16(data[16:64]) && u16(10) == figureCRC16(data[64:])
		return data, blank, valid
	}
	a, blankA, goodA := read(0)
	b, blankB, goodB := read(1)
	if a == nil || b == nil {
		return "", false
	}
	if blankA && blankB {
		return "", true
	}
	if !goodA && !goodB {
		return "", false
	}
	area := a
	if goodB && (!goodA || a[9]+1 == b[9]) {
		area = b
	}
	id, variant := area[16], area[0] == 1 && area[7] == area[16]
	if id == 0 {
		return "", true
	}
	for _, v := range trapVillains {
		if v.id == id && v.variant == variant {
			return v.name, true
		}
	}
	for _, v := range trapVillains {
		if v.id == id {
			return v.name, true
		}
	}
	return "", true
}
