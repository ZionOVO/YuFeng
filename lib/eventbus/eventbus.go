package eventbus

import (
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// readyTimeout 是等待内嵌 NATS 消息服务器就绪的上限；超时视为启动失败。
const readyTimeout = 5 * time.Second

// Bus 是事件总线封装。
type Bus struct {
	ns        *server.Server
	nc        *nats.Conn
	js        nats.JetStreamContext
	fileStore bool
}

// NewEmbedded 启动使用内存持久流的内嵌 NATS 消息服务器并连接。
func NewEmbedded(host string, port int) (*Bus, error) {
	return NewEmbeddedStore(host, port, "")
}

// NewEmbeddedStore 启动带持久流存储目录的内嵌 NATS 消息服务器。
// storeDir 非空时，进程重启后仍可恢复未确认消息。
func NewEmbeddedStore(host string, port int, storeDir string) (*Bus, error) {
	opts := &server.Options{
		Host:               host,
		Port:               port,
		NoLog:              true,
		NoSigs:             true,
		JetStream:          true,
		StoreDir:           storeDir,
		JetStreamMaxMemory: 64 << 20,
		JetStreamMaxStore:  64 << 20,
	}
	ns, err := server.NewServer(opts)
	if err != nil {
		return nil, fmt.Errorf("eventbus: create server: %w", err)
	}
	go ns.Start()
	if !ns.ReadyForConnections(readyTimeout) {
		ns.Shutdown()
		return nil, errors.New("eventbus: server not ready")
	}
	nc, err := nats.Connect(ns.ClientURL())
	if err != nil {
		ns.Shutdown()
		return nil, fmt.Errorf("eventbus: connect: %w", err)
	}
	b := &Bus{ns: ns, nc: nc, fileStore: storeDir != ""}
	if err := b.initJetStream(); err != nil {
		b.Close()
		return nil, err
	}
	return b, nil
}

// NewExternal 连接部署域提供的外部 NATS 消息服务器。
func NewExternal(url string) (*Bus, error) {
	nc, err := nats.Connect(url)
	if err != nil {
		return nil, fmt.Errorf("eventbus: connect external: %w", err)
	}
	b := &Bus{nc: nc}
	if err := b.initJetStream(); err != nil {
		b.Close()
		return nil, err
	}
	return b, nil
}

// Publish 发布一条事件。总线未连接时返回错误而非静默丢弃——
// 事件是审计与治理的数据源，无声丢失比发布失败更难察觉。
func (b *Bus) Publish(subject string, data []byte) error {
	if b == nil || b.nc == nil {
		return errors.New("eventbus: bus is not connected")
	}
	return b.nc.Publish(subject, data)
}

// Close 关闭连接与内嵌服务。
func (b *Bus) Close() {
	if b == nil {
		return
	}
	if b.nc != nil {
		_ = b.nc.Flush()
		b.nc.Close()
	}
	if b.ns != nil {
		b.ns.Shutdown()
		b.ns.WaitForShutdown()
	}
}
