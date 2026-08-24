// yufeng-dataplane 是技术人员可选手动启动的本机 Edge 健康监督器。
// 它不安装、创建、启动、重建、升级或卸载 Edge，也不持有 Docker。
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"yufeng/lib/dataplane"
	"yufeng/lib/kernel"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:19091", "本机只读监督器管理地址")
	probeURL := flag.String("edge-ready-url", "http://127.0.0.1:19092/ready", "已人工启动 Edge 的就绪地址")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	supervisor := &dataplane.Supervisor{Addr: *addr, ProbeURL: *probeURL}
	server := &http.Server{
		Addr: supervisor.ListenAddr(), Handler: supervisor.Handler(),
		ReadHeaderTimeout: kernel.HTTPReadHeaderTimeout, ReadTimeout: kernel.HTTPReadTimeout,
		WriteTimeout: kernel.HTTPWriteTimeout,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe() }()
	log.Printf("%s 只读监督口 %s", dataplane.BinaryName, server.Addr)
	select {
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), kernel.HTTPWriteTimeout)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}
}
