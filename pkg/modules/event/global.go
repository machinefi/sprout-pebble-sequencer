package event

import (
	"context"
	"encoding/binary"
	"fmt"
	"sort"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/google/uuid"
	"github.com/pkg/errors"
	"github.com/xoctopus/x/misc/must"

	"github.com/machinefi/sprout-pebble-sequencer/pkg/contexts"
	"github.com/machinefi/sprout-pebble-sequencer/pkg/middlewares/alert"
	"github.com/machinefi/sprout-pebble-sequencer/pkg/middlewares/blockchain"
)

type Event interface {
	// Source returns event source type, eg: mqtt, blockchain
	Source() SourceType
	// Topic returns the event subscription topic
	Topic() string
	// Unmarshal used to parse message payload to event body
	Unmarshal(data any) error
	// Handle to process event
	Handle(ctx context.Context) error
}

// EventHasTopicData if an event has metadata in topic, such as device imei in topic
type EventHasTopicData interface {
	Event
	UnmarshalTopic(topic []byte) error
}

type EventHasBlockchainMeta interface {
	Event
	// ContractID returns contract id defined in enums package
	ContractID() string
	// EventName returns event name event handling
	EventName() string
}

var (
	gEventFactory map[string]func() Event
	gByteOrder    = binary.BigEndian
)

func registry(topic string, f func() Event) {
	if gEventFactory == nil {
		gEventFactory = make(map[string]func() Event)
	}
	_, ok := gEventFactory[topic]
	if ok {
		panic(errors.Errorf("topic %s reregisted", topic))
	}
	gEventFactory[topic] = f
}

func NewEvent(topic string) Event {
	f, ok := gEventFactory[topic]
	if ok {
		return f()
	}
	return nil
}

func Events() []Event {
	events := make([]Event, 0, len(gEventFactory))
	for _, f := range gEventFactory {
		events = append(events, f())
	}
	sort.Slice(events, func(i, j int) bool {
		return events[i].Topic() < events[j].Topic()
	})
	return events
}

func Handle(ctx context.Context, subtopic, topic string, data any) (err error) {
	v := gEventFactory[subtopic]()
	l := must.BeTrueV(contexts.LoggerFromContext(ctx))

	defer func() {
		ll := l.WithValues(
			"source", v.Source().String(),
			"topic", topic,
			"data", v,
		)
		if t, ok := data.(TxEventUnmarshaler); ok {
			ll = ll.WithValues("block", t.BlockNumber())
		}
		if t, ok := v.(WithIMEI); ok {
			ll = ll.WithValues("imei", t.GetIMEI())
		}
		if t, ok := v.(CanValidateSignature); ok {
			ll = ll.WithValues(
				"sig", t.Signature(),
				"sig_addr", t.Address(),
				"sig_hash", t.Hash(),
			)
		}
		if err != nil {
			ll.Error(err, "failed to handle event")
		} else {
			ll.Info("event handled")
		}
	}()

	if parser, ok := v.(EventHasTopicData); ok {
		if err = parser.UnmarshalTopic([]byte(topic)); err != nil {
			return err
		}
	}
	if err = v.Unmarshal(data); err != nil {
		return err
	}
	return v.Handle(ctx)
}

func InitRunner(ctx context.Context) func() {
	logger := must.BeTrueV(contexts.LoggerFromContext(ctx))
	return func() {
		if err := Init(ctx); err != nil {
			logger.Error(err, "event module initialize failed")
			panic(err)
		}
		logger.Info("event module initialized")
	}
}

func Init(ctx context.Context) error {
	var (
		l   = must.BeTrueV(contexts.LoggerFromContext(ctx))
		err error
	)
	for _, v := range Events() {
		switch v.Source() {
		case SOURCE_TYPE__MQTT:
			err = StartMqttEventConsuming(ctx, v)
		case SOURCE_TYPE__BLOCKCHAIN:
			err = StartChainEventConsuming(ctx, v)
		default:
			panic(errors.Errorf("unexpected event source type: %d", v))
		}
		if err != nil {
			return errors.Wrapf(err, "failed to start event consuming [topic:%s]", v.Topic())
		}
		l.Info("event monitor started", "source", v.Source().String(), "topic", v.Topic())
	}
	return nil
}

func StartMqttEventConsuming(ctx context.Context, v Event) error {
	mq := must.BeTrueV(contexts.MqttBrokerFromContext(ctx))

	c, err := mq.NewClient(v.Topic()+uuid.NewString(), v.Topic())
	if err != nil {
		return errors.Wrapf(err, "failed to new mqtt client")
	}
	err = c.Subscribe(func(_ mqtt.Client, message mqtt.Message) {
		_ = Handle(ctx, v.Topic(), message.Topic(), message.Payload())
	})
	return errors.Wrap(err, "failed to start mqtt subscribing")
}

func StartChainEventConsuming(ctx context.Context, e Event) error {
	v, ok := e.(EventHasBlockchainMeta)
	must.BeTrueWrap(ok, "expect blockchain source event impl `EventHasBlockchainMeta`")

	bc := must.BeTrueV(contexts.BlockchainFromContext(ctx))
	contract := bc.ContractByID(v.ContractID())
	if contract == nil {
		return errors.Errorf("contract not found: [contract: %s]", v.ContractID())
	}
	monitor := bc.Monitor(v.ContractID(), v.EventName())
	if monitor == nil {
		return errors.Errorf("monitor not found: [contract: %s] [event: %s]", v.ContractID(), v.EventName())
	}

	sub, err := monitor.Watch(
		&blockchain.WatchOptions{SubID: "sprout-seq"},
		func(sub blockchain.Subscription, tx *types.Log) {
			_ = Handle(ctx, v.Topic(), v.Topic(), &TxEventParser{contract, tx})
		},
	)
	if err != nil {
		return errors.Wrapf(err, "failed to subscribe tx log: %s", "sprout-seq")
	}

	nc, _ := contexts.LarkAlertFromContext(ctx)
	if nc != nil && !nc.IsZero() {
		go func(nc *alert.LarkAlert, sub blockchain.Subscription, m *blockchain.Monitor) {
			err := <-sub.Err()
			_ = nc.Push(
				"chain subscriber stopped",
				fmt.Sprintf("\nmonitor: %s\nsubscriber: %s\n%v", m.Name(), sub.ID(), err),
			)
		}(nc, sub, monitor)
	}
	return nil
}
