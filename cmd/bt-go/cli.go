//go:build !desktop

package main

import (
	"flag"
	"log"

	btgo "bt-go"
)

func main() {
	addr := flag.String("addr", btgo.Getenv("BT_GO_ADDR", ":8088"), "HTTP listen address")
	dataDir := flag.String("dir", btgo.Getenv("BT_GO_DIR", "./downloads"), "download directory")
	listenPort := flag.Int("port", btgo.GetenvInt("BT_GO_LISTEN_PORT", 42069), "BitTorrent listen port")
	flag.Parse()

	if err := btgo.RunHTTP(*addr, *dataDir, *listenPort); err != nil {
		log.Fatal(err)
	}
}
