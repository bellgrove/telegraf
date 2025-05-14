package abb_rws

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMsgDecode(t *testing.T) {
	plugin := &AbbRws{}

	msgIn := []byte(`<?xml version="1.0" encoding="utf-8"?><html xmlns="http://www.w3.org/1999/xhtml"> <head> <title>Event</title><base href="http://10.209.3.22:80/"/> </head> <body>  <div class="state"><a href="subscription/9" rel="group"></a> <ul> <li class="ios-signalstate-ev" title="EtherNetIP/Local_IO/Local_IO_0_DO4"><a href="/rw/iosystem/signals/EtherNetIP/Local_IO/Local_IO_0_DO4;state" rel="self"/><span class="lvalue">0</span><span class="lstate">not simulated</span></li>  </ul> </div> </body></html>`)
	// msgIn := []byte(`<head> <title>Event</title><base href="http://10.209.3.22:80/"/> </head> <body>  <div class="state"><a href="subscription/9" rel="group"></a> <ul> <li class="ios-signalstate-ev" title="EtherNetIP/Local_IO/Local_IO_0_DO4"><a href="/rw/iosystem/signals/EtherNetIP/Local_IO/Local_IO_0_DO4;state" rel="self"/><span class="lvalue">0</span><span class="lstate">not simulated</span></li>  </ul> </div> </body></html>`)

	// msgIn := []byte(`
	//     <?xml version="1.0" encoding="utf-8"?>
	// 	<html>
	// 	  <body>
	// 		<FullName>Grace R. Emlin</FullName>
	// 		<Company>Example Inc.</Company>
	// 		<li where="home">
	// 			<Addr>gre@example.com</Addr>
	// 		</li>
	// 		<li where='work'>
	// 			<Addr>gre@work.com</Addr>
	// 		</li>
	// 		<Group>
	// 			<Value>Friends</Value>
	// 			<Value>Squash</Value>
	// 		</Group>
	// 		<City>Hanga Roa</City>
	// 		<State>Easter Island</State>
	// 	  </body>
	// 	</html>
	// `)

	p, err := plugin.parseMsg(msgIn)
	require.NoError(t, err)
	require.Contains(t, p, "source")
	require.Equal(t, "ios-signalstate-ev", p["source"])
}
