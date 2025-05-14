//go:generate ../../../tools/readme_config_includer/generator
package abb_rws

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"io"
	"log"
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

	conn *ws.Conn
	acc  telegraf.Accumulator

	client *http.Client
	jar    http.CookieJar

	parser telegraf.Parser
	// conn    *amqp.Connection
	wg      *sync.WaitGroup
	cancel  context.CancelFunc
	decoder internal.ContentDecoder
}

func (*externalAuth) Mechanism() string {
	return "EXTERNAL"
}

func (*externalAuth) Response() string {
	return "\000"
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

func (a *AbbRws) Start(acc telegraf.Accumulator) error {
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
	a.Log.Info("Login request: ", resp.Status, resp.Header)

	// Create queues if needed
	// Create subscription - with returned params
	// Connect(URL string, abbX string, session string)
	// subReq := "resources=1&1=/rw/iosystem/signals/Virtual1/Board1/di1;state&1-p=0&resources=2&2=/rw/iosystem/signals/Virtual1/Board1/di2;state&2-p=0"
	// subReq := "resources=1&1=/rw/elog/0&1-p=1"

	subReq := "resources=1&1=/rw/iosystem/signals/EtherNetIP/Local_IO/Local_IO_0_DO4;state&1-p=1&resources=2&2=/rw/elog/0&2-p=1&resources=3&3=/rw/dipc/PC_SDK_Q&3-p=1"
	resp, err = a.client.Post(a.Host+"/subscription", "Content-Type: application/x-www-form-urlencoded", bytes.NewBufferString(subReq))
	if err != nil {
		return fmt.Errorf("unable to create subscription: %w", err)
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatal(err)
	}
	bodyString := string(bodyBytes)
	a.Log.Info("Got response %s, %s, %s", resp.Status, resp.Header, bodyString)

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

func (*AbbRws) Gather(_ telegraf.Accumulator) error {
	return nil
}

func (w *AbbRws) Connect(URL *url.URL, cookies []*http.Cookie) error {
	w.Log.Debug("Connecting to websocket: ", URL)

	tlsCfg, err := w.ClientConfig.TLSConfig()
	if err != nil {
		return fmt.Errorf("error creating TLS config: %w", err)
	}

	dialProxy, err := w.HTTPProxy.Proxy()
	if err != nil {
		return fmt.Errorf("error creating proxy: %w", err)
	}

	dialer := &ws.Dialer{
		Proxy:            dialProxy,
		HandshakeTimeout: time.Duration(w.ConnectTimeout),
		TLSClientConfig:  tlsCfg,
		Subprotocols:     []string{"robapi2_subscription"},
		Jar:              w.jar,
	}

	if w.Socks5ProxyEnabled {
		netDialer, err := w.Socks5ProxyConfig.GetDialer()
		if err != nil {
			return fmt.Errorf("error connecting to socks5 proxy: %w", err)
		}
		dialer.NetDial = netDialer.Dial
	}

	headers := http.Header{}
	for k, v := range w.Headers {
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

	w.conn = conn
	go w.read(conn)

	return nil
}

func (w *AbbRws) read(conn *ws.Conn) {
	defer func() { _ = conn.Close() }()
	// if w.ReadTimeout > 0 {
	// 	if err := conn.SetReadDeadline(time.Now().Add(time.Duration(w.ReadTimeout))); err != nil {
	// 		w.Log.Errorf("error setting read deadline: %v", err)
	// 		return
	// 	}
	// 	conn.SetPingHandler(func(string) error {
	// 		err := conn.SetReadDeadline(time.Now().Add(time.Duration(w.ReadTimeout)))
	// 		if err != nil {
	// 			w.Log.Errorf("error setting read deadline: %v", err)
	// 			return err
	// 		}
	// 		return conn.WriteControl(ws.PongMessage, nil, time.Now().Add(time.Duration(w.WriteTimeout)))
	// 	})
	// }
	for {
		// Need to read a connection (to properly process pings from a server).
		_, msg, err := conn.ReadMessage()
		if err != nil {
			// Websocket connection is not readable after first error, it's going to error state.
			// In the beginning of this goroutine we have defer section that closes such connection.
			// After that connection will be tried to reestablish on next Write.
			if ws.IsUnexpectedCloseError(err, ws.CloseGoingAway, ws.CloseAbnormalClosure) {
				w.Log.Errorf("error reading websocket connection: %v", err)
			}
			return
		}
		resp, err := w.parseMsg(msg)
		w.Log.Info("recv: %s", resp)

		// example tags - host,
		w.acc.AddFields("rws_rmq", nil, nil)
		w.acc.AddFields("rws_io", nil, nil)
		w.acc.AddFields("rws_elog", nil, nil)

		// if w.ReadTimeout > 0 {
		// 	if err := conn.SetReadDeadline(time.Now().Add(time.Duration(w.ReadTimeout))); err != nil {
		// 		return
		// 	}
		// }
	}
}

type wsMsgPartVal struct {
	Class string `xml:"class,attr"`
	Value string `xml:",chardata"`
}

type wsMsgPart struct {
	Source  string         `xml:"class,attr"`
	Target  string         `xml:"title,attr"`
	Content []wsMsgPartVal `xml:"span"`
}

type wsMsg struct {
	Parts []wsMsgPart `xml:"body>div>ul>li"`
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

	ret["source"] = msgStruct.Parts[0].Source
	ret["target"] = msgStruct.Parts[0].Target
	ret["content"] = msgStruct.Parts[0].Content[0].Value

	return ret, nil
}

func (a *AbbRws) Stop() {
	// TODO: unsub, other message types, subscription list from config, send pings
	// Unsubscribe
	// Delete queue??
	a.conn.Close()
}
