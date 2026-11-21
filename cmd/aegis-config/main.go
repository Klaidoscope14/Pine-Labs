// Command aegis-config validates an AegisSDLC config file and exposes
// derived settings to scripts, so nothing is duplicated or hardcoded.
//
//	aegis-config validate <file>
//	aegis-config sandbox-args <file>   # hardened `docker run` flags
//	aegis-config sandbox-image <file>
package main

import (
	"fmt"
	"os"
	"strings"

	"aegis/internal/config"
	"aegis/internal/sandbox"
)

func usage() {
	fmt.Fprintln(os.Stderr, "usage: aegis-config validate|sandbox-args|sandbox-image <file>")
	os.Exit(2)
}

func main() {
	if len(os.Args) != 3 {
		usage()
	}
	cmd, path := os.Args[1], os.Args[2]
	cfg, err := config.LoadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "REJECTED %s\n%v\n", path, err)
		os.Exit(1)
	}
	switch cmd {
	case "validate":
		fmt.Printf("OK %s (environment=%s, endpoints=%d, hash=%s)\n",
			path, cfg.Deployment.Environment, len(cfg.Endpoints), cfg.Hash[:12])
	case "sandbox-args":
		args, err := sandbox.HardeningArgs(cfg.Sandbox)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(strings.Join(args, " "))
	case "sandbox-image":
		fmt.Println(cfg.Sandbox.Image)
	default:
		usage()
	}
}
