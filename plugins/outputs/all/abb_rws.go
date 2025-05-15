//go:build !custom || outputs || outputs.simpleoutput

package all

import _ "github.com/influxdata/telegraf/plugins/outputs/abb_rws" // register plugin
