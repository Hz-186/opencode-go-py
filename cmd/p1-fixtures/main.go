package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"os"

	"github.com/Hz-186/opencode-go-py/internal/p1fixture"
)

func main() {
	output := flag.String("output", "schema/json", "directory for deterministic P1 fixture artifacts")
	verify := flag.Bool("verify", false, "verify existing artifacts without changing them")
	flag.Parse()
	if *verify {
		if err := p1fixture.Verify(*output); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("verified %s/%s\n", *output, p1fixture.ManifestName)
		return
	}
	digest, err := p1fixture.Write(*output)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s/%s sha256=%s\n", *output, p1fixture.ManifestName, hex.EncodeToString(digest[:]))
}
