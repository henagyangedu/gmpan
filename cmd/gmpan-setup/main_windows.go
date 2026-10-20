//go:build windows

package main

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"gmpan/internal/setup"
)

func main() {
	flags := flag.NewFlagSet("gmpan-setup", flag.ContinueOnError)
	preview := flags.Bool("preview", false, "仅打开向导，不请求提权；安装仍需管理员权限")
	noBrowser := flags.Bool("no-browser", false, "测试使用：不自动打开系统浏览器")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return
	}
	if err := run(*preview, *noBrowser); err != nil {
		setup.ShowError(err.Error())
		os.Exit(1)
	}
}

func run(preview, noBrowser bool) error {
	if !preview {
		again, err := setup.EnsureElevated()
		if err != nil {
			return err
		}
		if again {
			return nil
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	bundle := filepath.Dir(exe)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	wizard := setup.New(bundle)
	wizard.Host = listener.Addr().String()
	url := "http://" + wizard.Host + "/#" + wizard.Token
	// The fragment is never sent in HTTP requests or written to installation logs.
	fmt.Println(url)
	if noBrowser {
		// Test access file stays next to this local executable; it is not a deployment artifact.
		if err := setup.SaveTestURL(filepath.Join(bundle, ".setup-test-url"), url); err != nil {
			return err
		}
	} else if err := setup.OpenURL(url); err != nil {
		return err
	}
	server := &http.Server{Handler: wizard.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	wizard.Quit = func() { _ = server.Close() }
	err = server.Serve(listener)
	if noBrowser {
		_ = os.Remove(filepath.Join(bundle, ".setup-test-url"))
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
