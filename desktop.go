//go:build desktop && darwin

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	webview "github.com/webview/webview_go"
)

func main() {
	dataDir := flag.String("dir", getenv("BT_GO_DIR", "./downloads"), "download directory")
	listenPort := flag.Int("port", getenvInt("BT_GO_LISTEN_PORT", 42069), "BitTorrent listen port")
	flag.Parse()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("open local desktop service port: %v", err)
	}

	httpServer, addr, cleanup, err := runHTTPOnListener(ln, *dataDir, *listenPort)
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

	w := webview.New(false)
	defer w.Destroy()
	w.SetTitle("bt-go")
	w.SetSize(1080, 760, webview.HintNone)
	w.Navigate(fmt.Sprintf("http://%s/", addr))
	w.Run()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
}
