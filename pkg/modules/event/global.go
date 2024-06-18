package event

import (
	"context"
	"reflect"
	"sort"
	"strings"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/pkg/errors"
	"github.com/xoctopus/x/misc/must"
	"golang.org/x/exp/maps"

	"github.com/machinefi/sprout-pebble-sequencer/pkg/contexts"
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
	// Data returns the structured data can be parsed from tx log
	Data() any
}

func SubID(e EventHasBlockchainMeta) string {
	return strings.Join([]string{
		"SUB", e.ContractID(), strings.ToUpper(e.EventName()),
	}, "__")
}

var gEventFactory map[string]func() Event

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

func Topics() []string {
	topics := maps.Keys(gEventFactory)
	sort.Slice(topics, func(i, j int) bool {
		return topics[i] < topics[j]
	})
	return topics
}

func Events() []Event {
	events := make([]Event, 0, len(gEventFactory))
	for _, f := range gEventFactory {
		events = append(events, f())
	}
	return events
}

func Handle(ctx context.Context, subtopic, topic string, data any) error {
	factory := gEventFactory[subtopic]
	if factory == nil {
		return errors.Errorf("factory not found by topic: %s", subtopic)
	}
	event := factory()

	if parser, ok := event.(EventHasTopicData); ok {
		if err := parser.UnmarshalTopic([]byte(topic)); err != nil {
			return err
		}
	}
	if err := event.Unmarshal(data); err != nil {
		return err
	}
	return event.Handle(ctx)
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
		logger = must.BeTrueV(contexts.LoggerFromContext(ctx))
		broker = must.BeTrueV(contexts.MqttBrokerFromContext(ctx))
	)

	for _, v := range Events() {
		switch v.Source() {
		case SOURCE_TYPE__MQTT:
			client, err := broker.NewClient(v.Topic(), v.Topic())
			if err != nil {
				return errors.Wrapf(err, "failed to new mqtt client for topic %s", v.Topic())
			}
			err = client.Subscribe(func(_ mqtt.Client, message mqtt.Message) {
				err := Handle(ctx, v.Topic(), message.Topic(), message.Payload())
				if err != nil {
					logger.WithValues(
						"client", client.ID(),
						"topic", v.Topic(),
					).Error(err, "failed to handle mqtt message")
				}
			})
			if err != nil {
				return errors.Wrapf(err, "failed to subscribe topic: %s", v.Topic())
			}
			return nil
		case SOURCE_TYPE__BLOCKCHAIN:
			m, ok := v.(EventHasBlockchainMeta)
			must.BeTrueWrap(ok, "expect blockchain source event impl `EventHasBlockchainMeta`")

			if err := StartChainEventConsuming(ctx, m); err != nil {
				return errors.Wrapf(err, "failed to start tx consuming")
			}
		default:
			return errors.Errorf("unexpected event source type: %d", v)
		}
	}
	return nil
}

func StartChainEventConsuming(ctx context.Context, v EventHasBlockchainMeta) error {
	var (
		bc     = must.BeTrueV(contexts.BlockchainFromContext(ctx))
		logger = must.BeTrueV(contexts.LoggerFromContext(ctx))
	)

	contract := bc.ContractByID(v.ContractID())
	if contract == nil {
		return errors.Errorf("contract not found: [contract: %s]", v.ContractID())
	}

	monitor := bc.Monitor(v.ContractID(), v.EventName())
	if monitor == nil {
		return errors.Errorf("monitor not found: [contract: %s] [event: %s]", v.ContractID(), v.EventName())
	}

	subid := SubID(v)
	sink := make(chan *types.Log, 32)
	sub, err := monitor.Watch(blockchain.WatchOptions{SubID: subid}, sink)
	if err != nil {
		return errors.Wrapf(err, "failed to subscribe tx log: %s", subid)
	}

	go func() {
		defer sub.Unsubscribe()
		for {
			select {
			case err = <-sub.Err():
				logger.Error(err, "subscribe failed", "subtopic", SubID(v))
				return
			case l := <-sink:
				data := v.Data()
				if err := contract.ParseTxLog(v.EventName(), l, data); err != nil {
					logger.Error(
						err, "failed to parse tx log: [tx: %s] [event: %s] [data: %s]",
						l.TxHash, v.EventName(), reflect.TypeOf(data),
					)
					continue
				}
				if err := Handle(ctx, v.Topic(), "", data); err != nil {
					logger.Error(
						err, "failed to handle event data: [tx: %s] [event: %s] [data: %s]",
						l.TxHash, v.EventName(), reflect.TypeOf(data),
					)
				}
			}
		}
	}()
	return nil
}