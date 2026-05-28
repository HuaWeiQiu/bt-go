//go:build desktop && !darwin && !windows

package main

import (
	"flag"
	"log"
)

func main() {
	addr := flag.String("addr", getenv("BT_GO_ADDR", ":8088"), "HTTP listen address")
	dataDir := flag.String("dir", getenv("BT_GO_DIR", "./downloads"), "download directory")
	listenPort := flag.Int("port", getenvInt("BT_GO_LISTEN_PORT", 42069), "BitTorrent listen port")
	flag.Parse()

	log.Printf("desktop tag on this platform runs the HTTP UI; open http://127.0.0.1%s", *addr)
	if err := runHTTP(*addr, *dataDir, *listenPort); err != nil {
		log.Fatal(err)
	}
}
