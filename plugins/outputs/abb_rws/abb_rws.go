// /go:generate ../../../tools/readme_config_includer/generator
package abb_rws

// abb_rws.go

import (
	"bytes"
	_ "embed"
	"fmt"
	"net/http"
	"net/http/cookiejar"

	"github.com/icholy/digest"
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
	Host     string            `toml:"host"`
	Username config.Secret     `toml:"username"`
	Password config.Secret     `toml:"password"`
	Headers  map[string]string `toml:"headers"`
	Timeout  config.Duration   `toml:"timeout"`
	Log      telegraf.Logger   `toml:"-"`
	MessageQ MsgQueue
	tls.ClientConfig
	proxy.TCPProxy

	client       *http.Client
	jar          http.CookieJar
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
	username, err := s.Username.Get()
	if err != nil {
		return fmt.Errorf("getting username failed: %w", err)
	}
	defer username.Destroy()

	password, err := s.Password.Get()
	if err != nil {
		return fmt.Errorf("getting password failed: %w", err)
	}
	defer password.Destroy()

	s.jar, _ = cookiejar.New(nil)

	// Create HTTP client
	// Maybe make a request as a 'ping' kinda thing to test the connection and details
	s.client = &http.Client{
		Transport: &digest.Transport{
			Username: username.String(),
			Password: password.String(),
		},
		Jar: s.jar,
	}

	return nil
}

func (s *AbbRws_RMQ) Close() error {
	// Close any connections here.
	// Write will not be called once Close is called, so there is no need to synchronize.
	if s.client != nil {
		s.client.CloseIdleConnections()
	}
	return nil
}

// Write should write immediately to the output, and not buffer writes
// (Telegraf manages the buffer for you). Returning an error will fail this
// batch of writes and the entire batch will be retried automatically.
func (s *AbbRws_RMQ) Write(metrics []telegraf.Metric) error {
	for _, metric := range metrics {
		// Try and map matric to something - like RWS message or a IO
		if metric.HasTag("rws_endpoint") { // This could be /rw/dipc/QUEUENAME for RMQ message - or /rw/io for an IO, for example
			metric.Accept()
			err := s.WriteRMQ(metric)
			return err
		} else {
			metric.Reject()
		}
	}
	return nil
}

func (s *AbbRws_RMQ) WriteRMQ(metric telegraf.Metric) error {
	// Extract message vars
	s.Log.Info("New RMQ:", metric)
	endpoint, has_endpoint := metric.GetTag("endpoint") // /rw/dipc/testq
	src_queue_name, has_src := metric.GetTag("src_queue")
	userdef, has_userdef := metric.GetField("userdef")
	message, has_message := metric.GetField("message")

	if !(has_endpoint && has_message && has_src && has_userdef) {
		metric.Drop()
		return fmt.Errorf("message does not have all required fields: end: %t, src: %t, userdef: %t, msg: %t", has_endpoint, has_src, has_userdef, has_message)
	}
	userdef_val, userdef_is_int := userdef.(int)
	if !userdef_is_int {
		metric.Drop()
		return fmt.Errorf("userdef is not an integer: userdef=%s", userdef)
	}

	// Check queue exists
	resp, err := s.client.Get(s.Host + endpoint)
	if resp.StatusCode != 200 {
		return fmt.Errorf("endpoint queue does not exist: %s", endpoint)
	}

	// Send message
	// Example Message: "dipc-src-queue-name=testq&dipc-cmd=111&dipc-userdef=222&dipc-msgtype=1&dipc-data=hello"
	fullMessage := fmt.Sprintf("dipc-src-queue-name=%s&dipc-cmd=%d&dipc-userdef=%d&dipc-msgtype=%d&dipc-data=%s", src_queue_name, 111, userdef_val, 1, message)

	resp, err = s.client.Post(s.Host+endpoint+"?action=dipc-send", "Content-Type: application/x-www-form-urlencoded", bytes.NewBufferString(fullMessage))
	if err != nil || resp.StatusCode != 200 {
		return fmt.Errorf("unable to send message: %d: %w", resp.StatusCode, err)
	}
	// defer resp.Body.Close()

	return nil
}

func init() {
	outputs.Add("abb_rws", func() telegraf.Output { return &AbbRws_RMQ{} })
}
