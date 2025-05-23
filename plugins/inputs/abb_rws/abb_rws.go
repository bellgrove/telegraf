//go:generate ../../../tools/readme_config_includer/generator
package abb_rws

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"io"
	"strings"
	"time"

	"encoding/xml"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sync"

	ws "github.com/gorilla/websocket"
	"github.com/icholy/digest"

	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/config"
	"github.com/influxdata/telegraf/internal"
	"github.com/influxdata/telegraf/plugins/common/proxy"
	"github.com/influxdata/telegraf/plugins/common/tls"
	"github.com/influxdata/telegraf/plugins/inputs"
)

//go:embed sample.conf
var sampleConfig string

var once sync.Once

type empty struct{}
type externalAuth struct{}

type semaphore chan empty

type AbbRws struct {
	Host           string                    `toml:"host"`
	RobotId        int                       `toml:"rob_id"`
	SubReqs        []subs                    `toml:"inputs"`
	Username       config.Secret             `toml:"username"`
	Password       config.Secret             `toml:"password"`
	ConnectTimeout config.Duration           `toml:"connect_timeout"`
	WriteTimeout   config.Duration           `toml:"write_timeout"`
	ReadTimeout    config.Duration           `toml:"read_timeout"`
	Headers        map[string]*config.Secret `toml:"headers"`
	Log            telegraf.Logger           `toml:"-"`

	proxy.HTTPProxy
	proxy.Socks5ProxyConfig
	tls.ClientConfig
	client  *http.Client
	jar     http.CookieJar
	conn    *ws.Conn
	acc     telegraf.Accumulator
	parser  telegraf.Parser
	wg      *sync.WaitGroup
	cancel  context.CancelFunc
	decoder internal.ContentDecoder
}

type subs struct {
	Target   string `toml:"target"`
	Priority int    `toml:"priority"`
}

type wsMsg struct {
	Parts []wsMsgPart `xml:"body>div>ul>li"`
}

type wsMsgPart struct {
	MsgType string         `xml:"class,attr"`
	Queue   wsMsgPartVal   `xml:"a"`
	Content []wsMsgPartVal `xml:"span"`
}
type wsMsgPartVal struct {
	Class string `xml:"class,attr"`
	Value string `xml:",chardata"`
	Href  string `xml:"href,attr"`
}

func (a *AbbRws) formatSubs(reqs []subs) (string, error) {
	// Example SubReq: "resources=1&1=/rw/iosystem/signals/EtherNetIP/Local_IO/Local_IO_0_DO4;state&1-p=1&resources=2&2=/rw/elog/0&2-p=1&resources=3&3=/rw/dipc/PC_SDK_Q&3-p=1"
	out := ""
	for i := 1; i <= len(reqs); i++ {
		out = out + fmt.Sprintf("resources=%d&%d=%s&%d-p=%d&", i, i, reqs[i-1].Target, i, reqs[i-1].Priority)
	}
	out, _ = strings.CutSuffix(out, "&")
	err := error(nil)

	return out, err
}

func (a *AbbRws) Start(acc telegraf.Accumulator) error {
	a.Log.Info("Starting RWS input")

	a.acc = acc
	urlHost, err := url.Parse(a.Host)

	username, err := a.Username.Get()
	if err != nil {
		return fmt.Errorf("getting username failed: %w", err)
	}
	defer username.Destroy()

	password, err := a.Password.Get()
	if err != nil {
		return fmt.Errorf("getting password failed: %w", err)
	}
	defer password.Destroy()

	a.jar, _ = cookiejar.New(nil)
	client := &http.Client{
		Transport: &digest.Transport{
			Username: username.String(),
			Password: password.String(),
		},
		Jar: a.jar,
	}

	a.client = &http.Client{
		Jar: a.jar,
	}

	a.Log.Debug("Login cookies: ", a.jar.Cookies(urlHost))
	// First connection to login
	// Needs digest auth
	resp, err := client.Get(a.Host + "/rw")
	if err != nil {
		return fmt.Errorf("unable to login: %w", err)
	}
	resp.Body.Close()
	// a.Log.Info("Login request: ", resp.Status, resp.Header)

	// Create queues if needed
	// Create subscription - with returned params
	// Connect(URL string, abbX string, session string)

	for _, src := range a.SubReqs {
		if resp, err = a.client.Get(a.Host + src.Target); resp.StatusCode > 299 {
			createQ := fmt.Sprintf("dipc-queue-name=%s&dipc-queue-size=%d&dipc-max-msg-size=%d", strings.TrimPrefix(src.Target, "/rw/dipc/"), 5, 444)
			a.client.Post(a.Host+"/rw/dipc?action=dipc-create", "Content-Type: application/x-www-form-urlencoded", bytes.NewBufferString(createQ))
		}
	}

	subReq, err := a.formatSubs(a.SubReqs[:])

	resp, err = a.client.Post(a.Host+"/subscription", "Content-Type: application/x-www-form-urlencoded", bytes.NewBufferString(subReq))
	if err != nil {
		return fmt.Errorf("unable to create subscription: %w", err)
	}
	defer resp.Body.Close()

	wsUrl, err := resp.Location()
	if err != nil {
		return fmt.Errorf("missing websocket location: %w", err)
	}

	err = a.Connect(wsUrl, a.jar.Cookies(urlHost))
	if err != nil {
		return fmt.Errorf("websocket connection failed: %w", err)
	}

	return nil
}

func (a *AbbRws) Connect(URL *url.URL, cookies []*http.Cookie) error {
	a.Log.Debug("Connecting to websocket: ", URL)

	tlsCfg, err := a.ClientConfig.TLSConfig()
	if err != nil {
		return fmt.Errorf("error creating TLS config: %w", err)
	}

	dialProxy, err := a.HTTPProxy.Proxy()
	if err != nil {
		return fmt.Errorf("error creating proxy: %w", err)
	}

	dialer := &ws.Dialer{
		Proxy:            dialProxy,
		HandshakeTimeout: time.Duration(a.ConnectTimeout),
		TLSClientConfig:  tlsCfg,
		Subprotocols:     []string{"robapi2_subscription"},
		Jar:              a.jar,
	}

	if a.Socks5ProxyEnabled {
		netDialer, err := a.Socks5ProxyConfig.GetDialer()
		if err != nil {
			return fmt.Errorf("error connecting to socks5 proxy: %w", err)
		}
		dialer.NetDial = netDialer.Dial
	}

	headers := http.Header{}
	for k, v := range a.Headers {
		secret, err := v.Get()
		if err != nil {
			return fmt.Errorf("getting header secret %q failed: %w", k, err)
		}

		headers.Set(k, secret.String())
		secret.Destroy()
	}

	// We need initial HTTP subscription request to get the URL actually
	conn, resp, err := dialer.Dial(URL.String(), headers)
	if err != nil {
		return fmt.Errorf("error dial: %w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		return fmt.Errorf("wrong status code while connecting to server: %d", resp.StatusCode)
	}

	a.conn = conn
	go a.read(conn)

	return nil
}

func (a *AbbRws) read(conn *ws.Conn) {
	defer func() { _ = conn.Close() }()
	if a.ReadTimeout > 0 {
		if err := conn.SetReadDeadline(time.Now().Add(time.Duration(a.ReadTimeout))); err != nil {
			a.Log.Errorf("error setting read deadline: %v", err)
			return
		}
		conn.SetPingHandler(func(string) error {
			err := conn.SetReadDeadline(time.Now().Add(time.Duration(a.ReadTimeout)))
			if err != nil {
				a.Log.Errorf("error setting read deadline: %v", err)
				return err
			}
			return conn.WriteControl(ws.PongMessage, nil, time.Now().Add(time.Duration(a.WriteTimeout)))
		})
	}
	for {
		// Need to read a connection (to properly process pings from a server).
		_, msg, err := conn.ReadMessage()
		if err != nil {
			// Websocket connection is not readable after first error, it's going to error state.
			// In the beginning of this goroutine we have defer section that closes such connection.
			// After that connection will be tried to reestablish on next Write.
			if ws.IsUnexpectedCloseError(err, ws.CloseGoingAway, ws.CloseAbnormalClosure) {
				a.Log.Errorf("error reading websocket connection: %v", err)
			}
			return
		}

		resp, err := a.parseMsg(msg)
		a.Log.Info("Message Recieved: ", resp)

		// Format response according to message type
		switch resp["msgtype"].(string) {
		case "dipc-msg-ev":
			// a.Log.Info("Got RMQ message")
			content := resp["content"].([]wsMsgPartVal)

			tags := map[string]string{"msgtype": resp["msgtype"].(string), "endpoint": resp["endpoint"].(string)}
			fields := make(map[string]interface{})

			for _, f := range content {
				fields[f.Class] = f.Value
			}

			name := fmt.Sprintf("rws_%d", a.RobotId)
			a.acc.AddFields(name, fields, tags)
			// a.Log.Info(fmt.Sprintf("MQTT Sent: %s, fields: %s, tags: %s", name, fields, tags))
		case "elog-message-ev":
			a.Log.Info("Got error message")

			eMsg, err := a.client.Get(a.Host + resp["endpoint"].(string))
			if err != nil {
				a.Log.Error("problem getting error details: ", err)
				return
			}
			defer eMsg.Body.Close()
			eMsgBytes, err := io.ReadAll(eMsg.Body)
			resp, err = a.parseMsg(eMsgBytes)
			if err != nil {
				a.Log.Error("problem parsing error message: ", err)
				return
			}

			a.Log.Info("Error details: ", resp)

			content := resp["content"].([]wsMsgPartVal)

			tags := map[string]string{"msgtype": resp["msgtype"].(string), "endpoint": resp["endpoint"].(string)}
			fields := make(map[string]interface{})

			for _, f := range content {
				fields[f.Class] = f.Value
			}

			name := resp["msgtype"].(string)
			a.acc.AddFields(name, fields, tags)
			// a.Log.Info(fmt.Sprintf("MQTT Sent: %s, fields: %s, tags: %s", name, fields, tags))
		case "ios-signalstate-ev":
			a.Log.Info("Got IO Message")

		default:
			a.Log.Error("message of unknown type")
		}

		if a.ReadTimeout > 0 {
			if err := conn.SetReadDeadline(time.Now().Add(time.Duration(a.ReadTimeout))); err != nil {
				return
			}
		}
	}
}

func (a *AbbRws) parseMsg(msg []byte) (map[string]interface{}, error) {
	// Get as a message of some sort?
	ret := make(map[string]interface{})

	msgStruct := wsMsg{}
	err := xml.Unmarshal(msg, &msgStruct)
	if err != nil {
		fmt.Printf("erro: %v", err)
		return nil, err
	}

	ret["msgtype"] = msgStruct.Parts[0].MsgType
	ret["endpoint"] = msgStruct.Parts[0].Queue.Href
	ret["content"] = msgStruct.Parts[0].Content

	return ret, nil
}

func (a *AbbRws) Stop() {
	// Unsubscribe, Delete queue??
	if a.conn != nil {
		err := a.conn.Close()
		if err != nil {
			a.Log.Errorf("error closing websocket connection: %v", err)
		}
	}
}

func (*AbbRws) SampleConfig() string {
	return sampleConfig
}

func (a *AbbRws) Init() error {
	return nil
}

func init() {
	inputs.Add("abb_rws", func() telegraf.Input { return &AbbRws{} })
}

func (a *AbbRws) Gather(_ telegraf.Accumulator) error {
	return nil
}
