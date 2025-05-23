// /go:generate ../../../tools/readme_config_includer/generator
package abb_rws

// abb_rws.go

import (
	"bytes"
	_ "embed"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"strconv"
	"strings"

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
	Host         string            `toml:"host"`
	Username     config.Secret     `toml:"username"`
	Password     config.Secret     `toml:"password"`
	RobotId      int               `toml:"rob_id"`
	TargetQueue  string            `toml:"target"`
	SenderName   string            `toml:"sender"`
	MsgByteLimit int               `toml:"msg_size"`
	Headers      map[string]string `toml:"headers"`
	Timeout      config.Duration   `toml:"timeout"`
	Log          telegraf.Logger   `toml:"-"`
	tls.ClientConfig
	proxy.TCPProxy

	client       *http.Client
	jar          http.CookieJar
	serializer   telegraf.Serializer
	sentMessages int
	encoder      internal.ContentEncoder

	attempts int
}

func (*AbbRws_RMQ) SampleConfig() string {
	return sampleConfig
}

// Init is for setup, and validating config.
func (s *AbbRws_RMQ) Init() error {
	s.attempts = 0
	return nil
}

func (s *AbbRws_RMQ) Connect() error {
	s.Log.Info("Starting RWS output")
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
	s.client = &http.Client{
		Transport: &digest.Transport{
			Username: username.String(),
			Password: password.String(),
		},
		Jar: s.jar,
	}

	// Verify the target queue is present
	resp, err := s.client.Get(s.Host + s.TargetQueue)
	if err != nil || resp.StatusCode > 299 {
		return fmt.Errorf("could not verify target queue (%d): %v", resp.StatusCode, err)
	}
	defer resp.Body.Close()

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
	s.Log.Info("New MQTT message, beginning write")

	// Limits number of write attempts
	s.attempts++
	if s.attempts > 4 {
		s.Log.Error("attempts exceeded, aborting write")
		s.attempts = 0
		return nil
	}

	for _, metric := range metrics {
		s.Log.Info("New metric: ", metric)

		// Get message type
		msgtype, has_type := metric.GetTag("tags_msgtype")
		if !has_type {
			return fmt.Errorf("cannot identify message type")
		}

		switch msgtype {
		case "dipc-msg-ev":
			// RMQ message
			return s.WriteRMQ(metric)
		case "ios-signalstate-ev":
			// IO signal change
			// No action needed
			return nil
		case "elog-message-ev", "elog-message":
			// Event Log message
			return s.WriteELog(metric)
		case "vis-fruit":
			// Vision system fruit update
			return s.WriteFruit(metric)
		default:
			return fmt.Errorf("message has unknown type: %s", msgtype)
		}
	}
	return nil
}

func (s *AbbRws_RMQ) WriteELog(metric telegraf.Metric) error {
	// Check event severity
	severity, has_sev := metric.GetField("fields_msgtype")
	if !has_sev {
		return fmt.Errorf("could not determine event severity")
	}

	// Create instruction message
	userdef_val := s.RobotId
	message := ""
	switch severity {
	case "0", "1":
		// Informational Event/State Change -> Update log and do nothing
		s.Log.Info("New Info Event: ", metric)
		return nil
	case "2":
		// Warning Event -> Hold until all clear
		s.Log.Error("New Warning Event: ", metric)
		message = "ePMLCommand;[C_Hold]"
	case "3":
		// Error Event -> Abort
		s.Log.Error("New Error Event: ", metric)
		message = "ePMLCommand;[C_Abort]"
	}

	// Send message to robot
	fullMessage := fmt.Sprintf("dipc-src-queue-name=%s&dipc-cmd=%d&dipc-userdef=%d&dipc-msgtype=%d&dipc-data=%s", s.SenderName, 111, userdef_val, 1, message)
	s.Log.Info("Trying to send message: ", fullMessage)

	if len(fullMessage) > s.MsgByteLimit {
		return fmt.Errorf("message is too long to send - max_bytes=%d, msg_bytes=%d", s.MsgByteLimit, len(fullMessage))
	}

	resp, err := s.client.Post(s.Host+s.TargetQueue+"?action=dipc-send", "Content-Type: application/x-www-form-urlencoded", bytes.NewBufferString(fullMessage))
	if err != nil || resp.StatusCode >= 300 {
		return fmt.Errorf("unable to send message: %d: %w", resp.StatusCode, err)
	}
	defer resp.Body.Close()

	s.Log.Info("Message Sent")

	return nil
}

const FruitMax int = 4  // Maximum number of fruit that can be sent in one RMQ message
const FruitSize int = 7 // The number of coordinates per fruit

func (s *AbbRws_RMQ) WriteFruit(metric telegraf.Metric) error {
	s.Log.Info("Writing fruit...")
	// Extract message vars
	fields := metric.FieldList()
	fruitArray := [FruitMax][FruitSize]float64{}

	for _, f := range fields {
		// Each field has the form:   "fields_fruitIdx_coordIdx":value
		idx := strings.Split(f.Key, "_")
		fruit, err1 := strconv.Atoi(idx[1])
		coord, err2 := strconv.Atoi(idx[2])
		if fruit > FruitMax-1 || err1 != nil || err2 != nil {
			continue
		}
		fruitArray[fruit][coord] = f.Value.(float64)
	}

	// Format as RMQ message
	userdef_val := s.RobotId
	message := fmt.Sprintf("Fruit{%d};%.6f", FruitMax, fruitArray)
	message = strings.ReplaceAll(message, " ", ",")
	message = strings.ReplaceAll(message, ".000000", ".0")
	fullMessage := fmt.Sprintf("dipc-src-queue-name=%s&dipc-cmd=%d&dipc-userdef=%d&dipc-msgtype=%d&dipc-data=%s", s.SenderName, 111, userdef_val, 1, message)
	s.Log.Info("Trying to send message: ", fullMessage)

	if len(fullMessage) > s.MsgByteLimit {
		return fmt.Errorf("message is too long to send - max_bytes=%d, msg_bytes=%d", s.MsgByteLimit, len(fullMessage))
	}

	resp, err := s.client.Post(s.Host+s.TargetQueue+"?action=dipc-send", "Content-Type: application/x-www-form-urlencoded", bytes.NewBufferString(fullMessage))
	if err != nil || resp.StatusCode >= 300 {
		return fmt.Errorf("unable to send message: %d: %v", resp.StatusCode, err)
	}
	defer resp.Body.Close()

	s.Log.Info("Message Sent")

	return nil
}

func (s *AbbRws_RMQ) WriteRMQ(metric telegraf.Metric) error {
	// Extract message vars
	userdef, has_userdef := metric.GetField("fields_dipc-userdef")
	message, has_message := metric.GetField("fields_dipc-data")

	// Check that the vars were all successfully read
	if !(has_message && has_userdef) {
		return fmt.Errorf("message does not have all required fields for RMQ:  has_userdef: %t, has_msg: %t", has_userdef, has_message)
	}

	// Check userdef is an int
	userdef_val, err := strconv.ParseInt(userdef.(string), 10, 64)
	if err != nil {
		return fmt.Errorf("userdef is not an integer: userdef=%s", userdef)
	}

	// Send message
	// Example Message: "dipc-src-queue-name=testq&dipc-cmd=111&dipc-userdef=222&dipc-msgtype=1&dipc-data=hello"
	fullMessage := fmt.Sprintf("dipc-src-queue-name=%s&dipc-cmd=%d&dipc-userdef=%d&dipc-msgtype=%d&dipc-data=%s", s.SenderName, 111, userdef_val, 1, message)
	s.Log.Info("Trying to send message: ", fullMessage)

	if len(fullMessage) > s.MsgByteLimit {
		return fmt.Errorf("message is too long to send - max_bytes=%d, msg_bytes=%d", s.MsgByteLimit, len(fullMessage))
	}

	resp, err := s.client.Post(s.Host+s.TargetQueue+"?action=dipc-send", "Content-Type: application/x-www-form-urlencoded", bytes.NewBufferString(fullMessage))
	if err != nil || resp.StatusCode >= 300 {
		return fmt.Errorf("unable to send message: %d: %v", resp.StatusCode, err)
	}
	defer resp.Body.Close()

	s.Log.Info("Message Sent")

	return nil
}

func init() {
	outputs.Add("abb_rws", func() telegraf.Output { return &AbbRws_RMQ{} })
}
