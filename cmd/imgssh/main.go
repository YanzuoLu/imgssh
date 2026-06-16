package main

import (
	"context"
	"fmt"
	"os"

	"github.com/YanzuoLu/imgssh/internal/imgssh"
)

func main() {
	code, err := imgssh.Run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "imgssh: %v\n", err)
	}
	os.Exit(code)
}
