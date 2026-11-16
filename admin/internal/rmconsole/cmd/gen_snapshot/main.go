package main

import (
	"fmt"
	"os"

	rollingmemory "github.com/ai-go-hub/ai-go-admin/internal/rmconsole"
)

func main() {
	if err := rollingmemory.GenerateLocomoStaticSnapshot(); err != nil {
		fmt.Fprintf(os.Stderr, "Error generating snapshot: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Snapshot generated successfully.")
}
