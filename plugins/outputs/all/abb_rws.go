//go:build !custom || outputs || outputs.abb_rws

package all

import _ "github.com/influxdata/telegraf/plugins/outputs/abb_rws" // register plugin
