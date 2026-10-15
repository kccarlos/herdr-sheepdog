//go:build ignore

// dummyqr prints the pairing QR as an SVG for the example address
// 192.168.1.20 and a made-up token: what install.gif shows in place of the
// real one (install-gif.sh swaps it into the setup page).
//
//	go run scripts/readme-media/launch/dummyqr.go > qr.svg
package main

import (
	"fmt"
	"os"

	"github.com/kccarlos/herdr-sheepdog/internal/pair"
)

func main() {
	link := pair.URL([]pair.Address{{URL: "http://192.168.1.20:7777", Kind: pair.KindLAN}}, "0123456789abcdef0123456789abcdef")
	svg, err := pair.SVG(link)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Print(svg)
}
