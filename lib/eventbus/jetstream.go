package eventbus

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
)

// 持久流与主题是发件箱投递和模型消费者的唯一约定。
const (
	StreamName             = "YUFENG"
	SubjectEvents          = "yufeng.events.accepted"
	SubjectAnalysisTickets = "yufeng.analysis.tickets"
	SubjectModel           = "yufeng.model.append"
	SubjectModelResults    = "yufeng.model.results"
	DurableEvents          = "yufeng-events"
	DurableModel           = "yufeng-model"
	jetStreamDuplicates    = 5 * time.Minute
	defaultFetchWait       = 2 * time.Second
)

// initJetStream 连接持久消息流，并幂等创建御锋使用的主题范围。
func (b *Bus) initJetStream() error {
	if b == nil || b.nc == nil {
		return errors.New("eventbus: bus is not connected")
	}
	js, err := b.nc.JetStream()
	if err != nil {
		return fmt.Errorf("eventbus: jetstream: %w", err)
	}
	b.js = js
	storage := nats.MemoryStorage
	if b.fileStore {
		storage = nats.FileStorage
	}
	if _, err = js.AddStream(&nats.StreamConfig{
		Name:       StreamName,
		Subjects:   []string{"yufeng.>"},
		Storage:    storage,
		Retention:  nats.LimitsPolicy,
		Duplicates: jetStreamDuplicates,
		MaxAge:     24 * time.Hour,
	}); err != nil && !isJSExists(err) {
		return fmt.Errorf("eventbus: add stream: %w", err)
	}
	return nil
}

// isJSExists 判断错误是否表示目标持久流已经存在。
func isJSExists(err error) bool {
	return err == nats.ErrStreamNameAlreadyInUse
}

// isJSConsumerExists 判断错误是否表示目标持久消费者已经存在。
func isJSConsumerExists(err error) bool {
	return err == nats.ErrConsumerNameAlreadyInUse
}

// PublishDurable 按消息标识去重写入 NATS 持久流；重复标识不产生第二条可消费消息。
func (b *Bus) PublishDurable(subject, msgID string, data []byte) error {
	if b == nil || b.js == nil {
		return errors.New("eventbus: jetstream is not connected")
	}
	if msgID == "" {
		return errors.New("eventbus: message id is required")
	}
	pctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := b.js.Publish(subject, data, nats.MsgId(msgID), nats.Context(pctx))
	return err
}

// FetchDurable 用持久消费者拉取一条。未 Ack 的消息在进程重启后仍可被同一 durable 取到。
func (b *Bus) FetchDurable(durable, filter string, wait time.Duration) (*nats.Msg, error) {
	if b == nil || b.js == nil {
		return nil, errors.New("eventbus: jetstream is not connected")
	}
	if wait <= 0 {
		wait = defaultFetchWait
	}
	if _, err := b.js.AddConsumer(StreamName, &nats.ConsumerConfig{
		Durable:       durable,
		FilterSubject: filter,
		AckPolicy:     nats.AckExplicitPolicy,
		AckWait:       1500 * time.Millisecond,
		DeliverPolicy: nats.DeliverAllPolicy,
	}); err != nil && !isJSConsumerExists(err) {
		return nil, fmt.Errorf("eventbus: add consumer: %w", err)
	}
	sub, err := b.js.PullSubscribe(filter, durable, nats.BindStream(StreamName), nats.ManualAck())
	if err != nil {
		return nil, fmt.Errorf("eventbus: pull subscribe: %w", err)
	}
	msgs, err := sub.Fetch(1, nats.MaxWait(wait))
	if err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return nil, nats.ErrTimeout
	}
	return msgs[0], nil
}

// Pending 返回持久消费者未确认条数。
func (b *Bus) Pending(durable, filter string) (int, error) {
	if b == nil || b.js == nil {
		return 0, errors.New("eventbus: jetstream is not connected")
	}
	info, err := b.js.ConsumerInfo(StreamName, durable)
	if err != nil {
		return 0, err
	}
	return int(info.NumPending) + info.NumAckPending, nil
}
