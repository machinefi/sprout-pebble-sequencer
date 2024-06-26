package event

import (
	"context"
	"strings"
	"time"

	"github.com/pkg/errors"

	"github.com/machinefi/sprout-pebble-sequencer/pkg/enums"
	"github.com/machinefi/sprout-pebble-sequencer/pkg/models"
)

func init() {
	f := func() Event { return &PebbleConfig{} }
	e := f()
	registry(e.Topic(), f)
}

type PebbleConfig struct {
	Imei   string
	Config string
}

func (e *PebbleConfig) Source() SourceType { return SOURCE_TYPE__BLOCKCHAIN }

func (e *PebbleConfig) Topic() string {
	return strings.Join([]string{
		"TOPIC", e.ContractID(), strings.ToUpper(e.EventName()),
	}, "__")
}

func (e *PebbleConfig) ContractID() string { return enums.CONTRACT__PEBBLE_DEVICE }

func (e *PebbleConfig) EventName() string { return "Config" }

func (e *PebbleConfig) Unmarshal(v any) error {
	return v.(TxEventUnmarshaler).UnmarshalTx(e.EventName(), e)
}

func (e *PebbleConfig) Handle(ctx context.Context) (err error) {
	defer func() { err = WrapHandleError(err, e) }()

	dev := &models.Device{ID: e.Imei, Config: e.Config}
	if err = UpdateByPrimary(ctx, dev, map[string]any{
		"config":     e.Config,
		"updated_at": time.Now(),
	}); err != nil {
		return errors.Wrapf(err, "failed to update device config: %s %s", dev.ID, dev.Config)
	}

	app := &models.AppV2{ID: e.Config}
	if err = FetchByPrimary(ctx, app); err != nil {
		return errors.Wrapf(err, "failed to fetch app_v2: %s", app.ID)
	}

	err = PublicMqttMessage(ctx,
		"pebble_config",
		"backend/"+e.Imei+"/config", e.Imei,
		app.Data,
	)
	return errors.Wrap(err, "failed to publish pebble_config response")
}
