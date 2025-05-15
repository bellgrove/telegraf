// /go:generate ../../../tools/readme_config_includer/generator
package abb_rws

// abb_rws.go

import (
	_ "embed"

	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/config"
	"github.com/influxdata/telegraf/internal"
	"github.com/influxdata/telegraf/plugins/common/proxy"
	"github.com/influxdata/telegraf/plugins/common/tls"
	"github.com/influxdata/telegraf/plugins/outputs"
)

//go:embed sample.conf
var sampleConfig string

type AbbRws_RMQ struct {
	Host         string            `toml:"host"`
	Brokers      []string          `toml:"brokers"`
	Username     config.Secret     `toml:"username"`
	Password     config.Secret     `toml:"password"`
	AuthMethod   string            `toml:"auth_method"`
	DeliveryMode string            `toml:"delivery_mode"`
	Headers      map[string]string `toml:"headers"`
	Timeout      config.Duration   `toml:"timeout"`
	Log          telegraf.Logger   `toml:"-"`
	MessageQ     MsgQueue
	tls.ClientConfig
	proxy.TCPProxy

	serializer   telegraf.Serializer
	sentMessages int
	encoder      internal.ContentEncoder
}

type MsgQueue struct {
	TrayQ  chan Tray
	FruitQ chan Fruit
}

type Tray struct {
	idx        byte
	latched    int64
	valid      bool
	full       bool
	fruitCount int
}

type Fruit struct {
	x   int
	y   int
	z   int
	cnt int64
}

func (*AbbRws_RMQ) SampleConfig() string {
	return sampleConfig
}

// Init is for setup, and validating config.
func (s *AbbRws_RMQ) Init() error {
	return nil
}

func (s *AbbRws_RMQ) Connect() error {
	// Make any connection required here
	return nil
}

func (s *AbbRws_RMQ) Close() error {
	// Close any connections here.
	// Write will not be called once Close is called, so there is no need to synchronize.
	return nil
}

// Write should write immediately to the output, and not buffer writes
// (Telegraf manages the buffer for you). Returning an error will fail this
// batch of writes and the entire batch will be retried automatically.
func (s *AbbRws_RMQ) Write(metrics []telegraf.Metric) error {
	for _, metric := range metrics {
		// write `metric` to the output sink here
	}
	return nil
}

func init() {
	outputs.Add("abb_rws", func() telegraf.Output { return &AbbRws_RMQ{} })
}
