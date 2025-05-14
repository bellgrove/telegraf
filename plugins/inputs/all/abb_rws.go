//go:build !custom || inputs || inputs.abb_rws

package all

import _ "github.com/influxdata/telegraf/plugins/inputs/abb_rws" // register plugin
