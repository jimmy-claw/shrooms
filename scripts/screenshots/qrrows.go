//go:build ignore

// Prints a QR code as rows of 0 and 1, the shape /invite/new hands the
// Basecamp view, for the demo the site's screenshots are taken from.
//
//	go run scripts/screenshots/qrrows.go TEXT
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"rsc.io/qr"
)

func main() {
	c, err := qr.Encode(os.Args[1], qr.M)
	if err != nil {
		panic(err)
	}
	rows := make([]string, c.Size)
	for y := range rows {
		b := make([]byte, c.Size)
		for x := range b {
			b[x] = '0'
			if c.Black(x, y) {
				b[x] = '1'
			}
		}
		rows[y] = string(b)
	}
	out, _ := json.Marshal(rows)
	fmt.Println(string(out))
}
