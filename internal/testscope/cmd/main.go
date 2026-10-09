package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/AndrMoiseev/stepan/internal/testscope"
)

func main() {
	event := flag.String("event", "", "GitHub event name")
	flag.Parse()
	if *event == "" {
		fmt.Fprintln(os.Stderr, "test scope requires -event")
		os.Exit(2)
	}
	scope := testscope.Classify(*event, flag.Args())
	fmt.Printf("git=%t\nprocess=%t\n", scope.Git, scope.Process)
}
