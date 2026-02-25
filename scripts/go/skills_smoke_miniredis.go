package main

import (
	"flag"
	"fmt"
	"log"

	"github.com/alicebob/miniredis/v2"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "listen address")
	password := flag.String("password", "", "optional redis password")
	flag.Parse()

	server := miniredis.NewMiniRedis()
	if err := server.StartAddr(*addr); err != nil {
		log.Fatalf("start miniredis: %v", err)
	}
	if *password != "" {
		server.RequireAuth(*password)
	}

	fmt.Println(server.Addr())
	select {}
}
