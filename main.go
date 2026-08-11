package main

import "os"

func main() {
	os.Exit(Run(os.Args[1:], os.Stdout, os.Stderr))
}
