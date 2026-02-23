package main

import (
	"net/http"
	"os"

	"github.com/alecthomas/kingpin/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/promslog"
	"github.com/prometheus/common/promslog/flag"
	"github.com/prometheus/exporter-toolkit/web"
	webflag "github.com/prometheus/exporter-toolkit/web/kingpinflag"
	nodecollector "github.com/prometheus/node_exporter/collector"
	"github.com/vinted/sonic-exporter/internal/collector"
)

func main() {
	// setup node exporter collectors through global kingpin flags
	kingpin.CommandLine.Parse([]string{
		"--collector.disable-defaults",
		"--collector.loadavg",
		"--collector.cpu",
		"--collector.diskstats",
		"--collector.filesystem",
		"--collector.meminfo",
		"--collector.time",
		"--collector.stat",
	})

	// New kingpin instance to prevent imported code from adding flags (node exporter)
	kp := kingpin.New("sonic-exporter", "Prometheus exporter for SONiC network switches")

	var (
		webConfig   = webflag.AddFlags(kp, ":9101")
		metricsPath = kp.Flag("web.telemetry-path", "Path under which to expose metrics.").Default("/metrics").String()
	)

	promslogConfig := &promslog.Config{}
	flag.AddFlags(kp, promslogConfig)
	kp.HelpFlag.Short('h')
	kp.UsageWriter(os.Stdout)
	kp.Parse(os.Args[1:])

	logger := promslog.New(promslogConfig)

	// SONiC collectors
	interfaceCollector := collector.NewInterfaceCollector(logger)
	hwCollector := collector.NewHwCollector(logger)
	crmCollector := collector.NewCrmCollector(logger)
	queueCollector := collector.NewQueueCollector(logger)
	lldpCollector := collector.NewLldpCollector(logger)
	prometheus.MustRegister(interfaceCollector)
	prometheus.MustRegister(hwCollector)
	prometheus.MustRegister(crmCollector)
	prometheus.MustRegister(queueCollector)
	if lldpCollector.IsEnabled() {
		prometheus.MustRegister(lldpCollector)
	}

	// Node exporter collectors
	nodeCollector, err := nodecollector.NewNodeCollector(logger,
		"loadavg",
		"cpu",
		"diskstats",
		"filesystem",
		"meminfo",
		"time",
		"stat",
	)
	if err != nil {
		logger.Error("Failed to create node collector", "error", err)
		os.Exit(1)
	}
	prometheus.MustRegister(nodeCollector)

	http.Handle(*metricsPath, promhttp.Handler())
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, err := w.Write([]byte(`<html>
             <head><title>Sonic Exporter</title></head>
             <body>
             <h1>Sonic Exporter</h1>
             <p><a href='` + *metricsPath + `'>Metrics</a></p>
             </body>
             </html>`))
		if err != nil {
			logger.Error("Error writing response", "error", err)
		}
	})
	srv := &http.Server{}
	if err := web.ListenAndServe(srv, webConfig, logger); err != nil {
		logger.Error("Error starting HTTP server", "error", err)
		os.Exit(1)
	}
}
