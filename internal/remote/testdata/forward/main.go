package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"strings"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: forward <bind:port> <host:port|/socket/path>")
		os.Exit(255)
	}
	ln, err := net.Listen("tcp", os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "bind [%s]: Address already in use\nCould not request local forwarding.\n", os.Args[1])
		os.Exit(255)
	}
	for {
		c, err := ln.Accept()
		if err != nil {
			os.Exit(255)
		}
		go proxy(c, os.Args[2])
	}
}

func proxy(c net.Conn, dest string) {
	defer c.Close()
	network := "tcp"
	if strings.HasPrefix(dest, "/") {
		network = "unix"
	}
	d, err := net.Dial(network, dest)
	if err != nil {
		return
	}
	defer d.Close()
	go io.Copy(d, c)
	io.Copy(c, d)
}
