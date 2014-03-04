package ftp

import "unicode/utf8"

// ISO8859_15ToUTF8 converts an ISO-8859-15 string to UTF-8 encoding
func ISO8859_15ToUTF8(s string) string {
	var rn rune
	u := make([]rune, len(s))
	for i := 0; i < len(u); i++ {
		r := int(s[i])
		switch r {
		case 0xA4:
			rn = 0x20AC // EURO SIGN
		case 0xA6:
			rn = 0x0160 // LATIN CAPITAL LETTER S WITH CARON
		case 0xA8:
			rn = 0x0161 // LATIN SMALL LETTER S WITH CARON
		case 0xB4:
			rn = 0x017D // LATIN CAPITAL LETTER Z WITH CARON
		case 0xB8:
			rn = 0x017E // LATIN SMALL LETTER Z WITH CARON
		case 0xBC:
			rn = 0x0152 // LATIN CAPITAL LIGATURE OE
		case 0xBD:
			rn = 0x0153 // LATIN SMALL LIGATURE OE
		case 0xBE:
			rn = 0x0178 // LATIN CAPITAL LETTER Y WITH DIAERESIS
		default:
			rn = rune(r)
		}
		u[i] = rn
	}
	return string(u)
}

// UTF8ToISO8859_15 converts a UTF-8 string to ISO-8859-15 encoding
func UTF8ToISO8859_15(c string) string {
	var b byte
	s := make([]byte, utf8.RuneCountInString(c))
	si := 0
	for i, w := 0, 0; i < len(c); i += w {
		r, width := utf8.DecodeRuneInString(c[i:])
		w = width
		switch r {
		case 0x20AC:
			b = 0xA4 // EURO SIGN
		case 0x0160:
			b = 0xA6 // LATIN CAPITAL LETTER S WITH CARON
		case 0x0161:
			b = 0xA8 // LATIN SMALL LETTER S WITH CARON
		case 0x017D:
			b = 0xB4 // LATIN CAPITAL LETTER Z WITH CARON
		case 0x017E:
			b = 0xB8 // LATIN SMALL LETTER Z WITH CARON
		case 0x0152:
			b = 0xBC // LATIN CAPITAL LIGATURE OE
		case 0x0153:
			b = 0xBD // LATIN SMALL LIGATURE OE
		case 0x0178:
			b = 0xBE // LATIN CAPITAL LETTER Y WITH DIAERESIS
		default:
			b = byte(r)
		}
		s[si] = b
		si++
	}
	//fmt.Printf("%x\n", s)
	return string(s)
}
