// Example: use pig as a library. Run with: go run ./examples/sdk "list the files here"
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/tobyjackson/pig/runtime"
)

func main() {
	s, err := runtime.New(runtime.Options{Ephemeral: true, Mode: "print"})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer s.Close()

	// Print text as it streams and name each tool the model runs.
	s.Subscribe(func(e runtime.Event) {
		switch e.Type {
		case "message_update":
			if e.Update != nil && e.Update.Type == "text_delta" {
				fmt.Print(e.Update.Delta)
			}
		case "tool_execution_start":
			fmt.Printf("\n[%s %s]\n", e.ToolName, string(e.Args))
		}
	})

	prompt := strings.Join(os.Args[1:], " ")
	if prompt == "" {
		prompt = "What files are in the current directory?"
	}
	if err := s.Prompt(context.Background(), prompt); err != nil {
		fmt.Fprintln(os.Stderr, "\nerror:", err)
	}
	fmt.Println()
}
