//go:build desktop && windows

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	btgo "bt-go"
)

func main() {
	dataDir := flag.String("dir", btgo.Getenv("BT_GO_DIR", "./downloads"), "download directory")
	listenPort := flag.Int("port", btgo.GetenvInt("BT_GO_LISTEN_PORT", 42069), "BitTorrent listen port")
	flag.Parse()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("open local desktop service port: %v", err)
	}

	httpServer, addr, cleanup, err := btgo.RunHTTPOnListener(ln, *dataDir, *listenPort)
	if err != nil {
		log.Fatal(err)
	}
	defer cleanup()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	url := fmt.Sprintf("http://%s/", addr)
	if err := openWindowsDesktop(url); err != nil {
		log.Printf("open desktop page failed: %v", err)
		log.Printf("open this URL manually: %s", url)
	}

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}
}

func openWindowsDesktop(url string) error {
	edgeArgs := []string{
		"--app=" + url,
		"--new-window",
	}
	if err := exec.Command("cmd", "/c", "start", "", "msedge", edgeArgs[0], edgeArgs[1]).Start(); err == nil {
		return nil
	}
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}
