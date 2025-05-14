package abb_rws

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMsgDecode(t *testing.T) {
	plugin := &AbbRws{}

	// msgIn := []byte(`<?xml version="1.0" encoding="utf-8"?><html xmlns="http://www.w3.org/1999/xhtml"> <head> <title>Event</title><base href="http://10.209.3.22:80/"/> </head> <body>  <div class="state"><a href="subscription/9" rel="group"></a> <ul> <li class="ios-signalstate-ev" title="EtherNetIP/Local_IO/Local_IO_0_DO4"><a href="/rw/iosystem/signals/EtherNetIP/Local_IO/Local_IO_0_DO4;state" rel="self"/><span class="lvalue">0</span><span class="lstate">not simulated</span></li>  </ul> </div> </body></html>`)
	// msgIn := []byte(`<?xml version="1.0" encoding="utf-8"?><html xmlns="http://www.w3.org/1999/xhtml"><head><title>Event</title><base href="http://10.209.3.22:80/" /></head><body><div class="state"><a href="subscription/12" rel="group"></a><ul><li class="dipc-msg-ev" title="msg"><a href="/rw/dipc/PC_SDK_Q" rel="self" title="/rw/dipc/PC_SDK_Q"></a><span class="dipc-slotid">192</span><span class="dipc-data">ping;[TRUE]</span><span class="dipc-userdef">-1</span></li></ul></div></body></html>`)
	msgIn := []byte(`<?xml version="1.0" encoding="utf-8"?><html xmlns="http://www.w3.org/1999/xhtml"><head><title>Event</title><base href="http://10.209.3.22:80/" /></head><body><div class="state"><a href="subscription/12" rel="group"></a><ul><li class="elog-message-ev" title="message"><a href="/rw/elog/0/4050138" rel="self" /><span class="seqnum">4050138</span></li></ul></div></body></html>`)

	p, err := plugin.parseMsg(msgIn)
	require.NoError(t, err)
	require.Contains(t, p, "msgtype")
	require.Contains(t, p, "source")
	require.Contains(t, p, "content")
	// require.Equal(t, "ios-signalstate-ev", p["msgtype"])
	// require.Equal(t, "EtherNetIP/Local_IO/Local_IO_0_DO4", p["source"])
	// require.Equal(t, "dipc-msg-ev", p["msgtype"])
	// require.Equal(t, "/rw/dipc/PC_SDK_Q", p["source"])
	require.Equal(t, "elog-message-ev", p["msgtype"])
	require.Equal(t, "/rw/elog/0/4050138", p["source"])
}

func TestInputFormat(t *testing.T) {
	plugin := &AbbRws{}

	var subArray [3]subs
	subArray[2].Target = "/rw/dipc/PC_SDK_Q"
	subArray[2].Priority = 1
	subArray[0].Target = "/rw/iosystem/signals/EtherNetIP/Local_IO/Local_IO_0_DO4;state"
	subArray[0].Priority = 1
	subArray[1].Target = "/rw/elog/0"
	subArray[1].Priority = 1

	p, err := plugin.formatSubs(subArray[:])
	require.NoError(t, err)
	require.Equal(t, "resources=1&1=/rw/iosystem/signals/EtherNetIP/Local_IO/Local_IO_0_DO4;state&1-p=1&resources=2&2=/rw/elog/0&2-p=1&resources=3&3=/rw/dipc/PC_SDK_Q&3-p=1", p)
}
