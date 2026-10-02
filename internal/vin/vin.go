// Package vin validates North American Vehicle Identification Numbers.
//
// It is a separate package because several features need it: trucks now,
// and ELD devices later (they report the VIN read from the engine computer).
package vin

import "strings"

// Each character's value in the check-digit calculation. I, O and Q are never
// used in VINs (too easy to confuse with 1 and 0), so they are absent.
var values = map[byte]int{
	'0': 0, '1': 1, '2': 2, '3': 3, '4': 4, '5': 5, '6': 6, '7': 7, '8': 8, '9': 9,
	'A': 1, 'B': 2, 'C': 3, 'D': 4, 'E': 5, 'F': 6, 'G': 7, 'H': 8,
	'J': 1, 'K': 2, 'L': 3, 'M': 4, 'N': 5, 'P': 7, 'R': 9,
	'S': 2, 'T': 3, 'U': 4, 'V': 5, 'W': 6, 'X': 7, 'Y': 8, 'Z': 9,
}

// Position weights. Position 9 (index 8) is the check digit itself: weight 0.
var weights = [17]int{8, 7, 6, 5, 4, 3, 2, 10, 0, 9, 8, 7, 6, 5, 4, 3, 2}

// Normalize trims spaces and upper-cases a VIN.
func Normalize(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}

// Valid reports whether v (already normalized) is a well-formed VIN with a
// correct check digit.
func Valid(v string) bool {
	if len(v) != 17 {
		return false
	}
	sum := 0
	for i := 0; i < 17; i++ {
		val, ok := values[v[i]]
		if !ok {
			return false // includes I, O, Q, lower case and symbols
		}
		sum += val * weights[i]
	}
	return v[8] == checkChar(sum%11)
}

func checkChar(remainder int) byte {
	if remainder == 10 {
		return 'X'
	}
	return byte('0' + remainder)
}
